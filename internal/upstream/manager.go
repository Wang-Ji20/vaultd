// Package upstream connects to other MCP servers and enforces which of their
// tools may be used. No server is special: all of them are config entries.
package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hortensia/vaultd/internal/config"
)

// CallTimeout limits each call to an upstream tool.
const CallTimeout = 60 * time.Second

// ErrToolNotAvailable marks a tool that is hidden, locked or does not exist.
// The message is the same in all three cases, so a caller cannot use it to
// find out which tools exist.
var ErrToolNotAvailable = errors.New("tool not available")

// Manager owns the connections to all upstreams.
type Manager struct {
	version string
	log     *slog.Logger
	notify  func()

	mu    sync.Mutex
	conns map[string]*conn
}

// NewManager creates a manager with no upstreams. onToolsChanged runs whenever
// an upstream's tools may have changed; it may be called from any goroutine.
func NewManager(version string, logger *slog.Logger, onToolsChanged func()) *Manager {
	if err := killChildrenWithParent(); err != nil {
		logger.Warn("upstream children may outlive vaultd if it crashes", "error", err)
	}
	return &Manager{version: version, log: logger, notify: onToolsChanged, conns: map[string]*conn{}}
}

// Apply brings the upstreams in line with a config. It connects to nothing:
// connections open on first use. A change to only the allow or locked list
// applies at once and keeps the running connection.
func (m *Manager) Apply(upstreams []config.Upstream) {
	m.mu.Lock()
	var stale []*conn
	wanted := map[string]bool{}
	for _, cfg := range upstreams {
		wanted[cfg.Name] = true
		existing, found := m.conns[cfg.Name]
		if found && sameConnection(existing.policy(), cfg) {
			existing.setPolicy(cfg)
			continue
		}
		if found {
			stale = append(stale, existing)
		}
		m.conns[cfg.Name] = newConn(cfg, m.version, m.notify)
	}
	for name, existing := range m.conns {
		if !wanted[name] {
			stale = append(stale, existing)
			delete(m.conns, name)
		}
	}
	m.mu.Unlock()
	closeAll(stale)
}

func sameConnection(a, b config.Upstream) bool {
	a.Allow, a.Locked, b.Allow, b.Locked = nil, nil, nil, nil
	return reflect.DeepEqual(a, b)
}

func closeAll(conns []*conn) {
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.close()
		}()
	}
	wg.Wait()
}

// Close ends every connection and stops every child process.
func (m *Manager) Close() {
	m.mu.Lock()
	conns := make([]*conn, 0, len(m.conns))
	for _, c := range m.conns {
		conns = append(conns, c)
	}
	m.conns = map[string]*conn{}
	m.mu.Unlock()
	closeAll(conns)
}

func (m *Manager) lookup(name string) (*conn, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, found := m.conns[name]
	return c, found
}

func (m *Manager) all() []*conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*conn, 0, len(m.conns))
	for _, c := range m.conns {
		out = append(out, c)
	}
	return out
}

// Allowed is the one place that decides whether a tool may be used. Locked
// wins over allowed, even if validation was somehow bypassed.
func Allowed(cfg config.Upstream, tool string) bool {
	return slices.Contains(cfg.Allow, tool) && !slices.Contains(cfg.Locked, tool)
}

// Split parses a namespaced tool name. Only the first dot separates, since an
// upstream's own tool names may hold dots.
func Split(qualified string) (upstream, tool string, ok bool) {
	return strings.Cut(qualified, ".")
}

// Connect opens the connections of all upstreams that have allowed tools, at
// the same time, so that one slow upstream delays the others as little as
// possible. A failure only affects that upstream's tools.
func (m *Manager) Connect(ctx context.Context) {
	m.connect(ctx, func(c *conn) bool { return len(c.policy().Allow) > 0 })
}

// ConnectAll also opens upstreams with nothing allowed yet, so that the admin
// portal can show which tools they offer.
func (m *Manager) ConnectAll(ctx context.Context) {
	m.connect(ctx, func(*conn) bool { return true })
}

func (m *Manager) connect(ctx context.Context, wanted func(*conn) bool) {
	var wg sync.WaitGroup
	for _, c := range m.all() {
		if !wanted(c) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A failure is recorded on the connection and shown in its status.
			_ = c.ensure(ctx)
		}()
	}
	wg.Wait()
}

// AllowedTools returns the visible tools under their namespaced names, from
// what is known right now. Descriptions and schemas are not changed.
func (m *Manager) AllowedTools() []*mcp.Tool {
	var out []*mcp.Tool
	for _, c := range m.all() {
		cfg := c.policy()
		_, tools := c.current()
		for _, tool := range tools {
			if Allowed(cfg, tool.Name) {
				renamed := *tool
				renamed.Name = cfg.Name + "." + tool.Name
				out = append(out, &renamed)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Call runs a namespaced tool. The upstream's result comes back unchanged.
// It returns ErrToolNotAvailable for anything not allowed, without contacting
// the upstream at all.
func (m *Manager) Call(ctx context.Context, qualified string, args json.RawMessage) (*mcp.CallToolResult, error) {
	name, tool, ok := Split(qualified)
	if !ok {
		return nil, ErrToolNotAvailable
	}
	c, found := m.lookup(name)
	if !found || !Allowed(c.policy(), tool) {
		return nil, ErrToolNotAvailable
	}
	if err := c.ensure(ctx); err != nil {
		return nil, fmt.Errorf("upstream %q is unavailable: %w", name, err)
	}
	session, tools := c.current()
	if session == nil {
		return nil, fmt.Errorf("upstream %q is unavailable: connection lost", name)
	}
	if !slices.ContainsFunc(tools, func(t *mcp.Tool) bool { return t.Name == tool }) {
		return nil, ErrToolNotAvailable
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("upstream %q: %w", name, err)
	}
	return result, nil
}

// Info describes one upstream for the admin portal.
type Info struct {
	Name      string
	Transport string
	Status    Status
	Error     string
	Tools     []ToolInfo
}

// ToolInfo describes one tool an upstream offers or the config names.
type ToolInfo struct {
	Name        string
	Description string
	Allowed     bool
	Locked      bool
}

// Status reports every upstream and every tool it offers, sorted by name.
func (m *Manager) Status() []Info {
	out := []Info{}
	for _, c := range m.all() {
		out = append(out, c.info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *conn) info() Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	info := Info{Name: c.cfg.Name, Transport: c.cfg.Transport, Status: c.status}
	if c.lastErr != nil {
		info.Error = c.lastErr.Error()
	}
	seen := map[string]bool{}
	for _, tool := range c.tools {
		seen[tool.Name] = true
		info.Tools = append(info.Tools, toolInfo(c.cfg, tool.Name, tool.Description))
	}
	// Locked tools are shown even before connecting, or if not currently offered.
	for _, name := range slices.Concat(c.cfg.Locked, c.cfg.Allow) {
		if !seen[name] {
			seen[name] = true
			info.Tools = append(info.Tools, toolInfo(c.cfg, name, ""))
		}
	}
	sort.Slice(info.Tools, func(i, j int) bool { return info.Tools[i].Name < info.Tools[j].Name })
	return info
}

func toolInfo(cfg config.Upstream, name, description string) ToolInfo {
	return ToolInfo{
		Name:        name,
		Description: description,
		Allowed:     Allowed(cfg, name),
		Locked:      slices.Contains(cfg.Locked, name),
	}
}

// Reconnect drops one upstream's connection and connects again immediately.
func (m *Manager) Reconnect(ctx context.Context, name string) error {
	c, found := m.lookup(name)
	if !found {
		return fmt.Errorf("no upstream named %q", name)
	}
	return c.reconnect(ctx)
}
