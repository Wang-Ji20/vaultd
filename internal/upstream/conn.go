package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hortensia/vaultd/internal/config"
)

const (
	backoffStart   = time.Second
	backoffMax     = 60 * time.Second
	stableUptime   = 60 * time.Second // a connection this old did not crash-loop
	connectTimeout = 15 * time.Second
)

// Status is the state of one upstream connection.
type Status string

// The states an upstream can be in.
const (
	StatusIdle       Status = "idle" // not used yet: connections are lazy
	StatusConnecting Status = "connecting"
	StatusConnected  Status = "connected"
	StatusError      Status = "error"
)

// conn is the connection to one upstream. It reconnects by itself after a
// failure, waiting longer each time.
type conn struct {
	version string
	notify  func() // called when the tool list may have changed

	mu          sync.Mutex
	cfg         config.Upstream
	session     *mcp.ClientSession
	tools       []*mcp.Tool
	status      Status
	lastErr     error
	backoff     time.Duration
	nextAttempt time.Time
	connectedAt time.Time
	inFlight    chan struct{} // closed when the running connect attempt ends
	retry       *time.Timer
	closed      bool
}

func newConn(cfg config.Upstream, version string, notify func()) *conn {
	return &conn{cfg: cfg, version: version, notify: notify, status: StatusIdle, backoff: backoffStart}
}

// ensure returns once the upstream is connected or the attempt has failed.
// During the wait after a failure it answers with that failure right away, so
// a dead upstream cannot slow down callers. Only one attempt runs at a time.
func (c *conn) ensure(ctx context.Context) error {
	for {
		c.mu.Lock()
		switch {
		case c.closed:
			c.mu.Unlock()
			return errors.New("upstream is closed")
		case c.session != nil:
			c.mu.Unlock()
			return nil
		case c.inFlight != nil:
			wait := c.inFlight
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		case time.Now().Before(c.nextAttempt):
			err := c.lastErr
			c.mu.Unlock()
			return err
		}
		c.inFlight = make(chan struct{})
		c.status = StatusConnecting
		cfg := c.cfg
		c.mu.Unlock()
		c.finishAttempt(c.connect(cfg))
	}
}

type attempt struct {
	session *mcp.ClientSession
	tools   []*mcp.Tool
	err     error
}

// connect uses its own timeout rather than the caller's context: if the app
// that triggered it hangs up, the connection is still useful to the next one.
func (c *conn) connect(cfg config.Upstream) attempt {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	transport, err := newTransport(cfg)
	if err != nil {
		return attempt{err: err}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "vaultd", Version: c.version}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { go c.refreshTools() },
	})
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return attempt{err: fmt.Errorf("connect: %w", err)}
	}
	tools, err := listTools(ctx, session)
	if err != nil {
		session.Close()
		return attempt{err: fmt.Errorf("list tools: %w", err)}
	}
	return attempt{session: session, tools: tools}
}

func listTools(ctx context.Context, session *mcp.ClientSession) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

func (c *conn) finishAttempt(result attempt) {
	c.mu.Lock()
	close(c.inFlight)
	c.inFlight = nil
	if c.closed {
		c.mu.Unlock()
		if result.session != nil {
			result.session.Close()
		}
		return
	}
	if result.err != nil {
		c.failLocked(result.err)
		c.mu.Unlock()
		return
	}
	c.session, c.tools = result.session, result.tools
	c.status, c.lastErr = StatusConnected, nil
	c.connectedAt = time.Now()
	c.nextAttempt = time.Time{}
	c.mu.Unlock()
	go c.watchSession(result.session)
	c.notify()
}

// failLocked records a failure and schedules the next attempt. The attempt runs
// even if nobody is asking, since that is what restarts a crashed child.
func (c *conn) failLocked(err error) {
	c.status, c.lastErr = StatusError, err
	wait := c.backoff
	c.backoff = min(c.backoff*2, backoffMax)
	c.nextAttempt = time.Now().Add(wait)
	if c.retry != nil {
		c.retry.Stop()
	}
	c.retry = time.AfterFunc(wait, func() {
		// A failure is recorded on the connection itself; nobody is waiting here.
		_ = c.ensure(context.Background())
	})
}

// watchSession waits for the connection to end, by crash or by network failure.
func (c *conn) watchSession(session *mcp.ClientSession) {
	waitErr := session.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.session != session {
		return
	}
	c.session = nil
	// Only a connection that lasted a while earns a fresh start. Otherwise a
	// child that crashes right after starting would restart every second.
	if time.Since(c.connectedAt) >= stableUptime {
		c.backoff = backoffStart
	}
	if waitErr == nil {
		waitErr = errors.New("connection ended")
	}
	c.failLocked(fmt.Errorf("disconnected: %w", waitErr))
}

func (c *conn) refreshTools() {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	tools, err := listTools(ctx, session)
	if err != nil {
		return // the old list stays; a dead connection is handled by watchSession
	}
	c.mu.Lock()
	if c.session == session {
		c.tools = tools
	}
	c.mu.Unlock()
	c.notify()
}

// reconnect drops the connection and tries again at once, ignoring the backoff.
func (c *conn) reconnect(ctx context.Context) error {
	c.mu.Lock()
	session := c.session
	c.session = nil
	c.backoff = backoffStart
	c.nextAttempt = time.Time{}
	if c.retry != nil {
		c.retry.Stop()
	}
	c.status = StatusIdle
	c.mu.Unlock()
	if session != nil {
		session.Close()
	}
	return c.ensure(ctx)
}

func (c *conn) close() {
	c.mu.Lock()
	c.closed = true
	session := c.session
	c.session = nil
	if c.retry != nil {
		c.retry.Stop()
	}
	c.mu.Unlock()
	if session != nil {
		session.Close()
	}
}

func (c *conn) setPolicy(cfg config.Upstream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = cfg
}

func (c *conn) policy() config.Upstream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

func (c *conn) current() (*mcp.ClientSession, []*mcp.Tool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session, c.tools
}

func newTransport(cfg config.Upstream) (mcp.Transport, error) {
	switch cfg.Transport {
	case "stdio":
		env, err := resolveAll(cfg.Env)
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = os.Environ()
		for key, value := range env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		return &mcp.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil
	case "http":
		headers, err := resolveAll(cfg.Headers)
		if err != nil {
			return nil, err
		}
		return &mcp.StreamableClientTransport{
			Endpoint:   cfg.URL,
			HTTPClient: &http.Client{Transport: headerTransport{headers}},
		}, nil
	}
	return nil, fmt.Errorf("unknown transport %q", cfg.Transport)
}

// resolveAll reads env: references. Errors name the variable, which is not a
// secret, and never a value.
func resolveAll(values map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(values))
	for key, value := range values {
		name, isRef := strings.CutPrefix(value, config.EnvPrefix)
		if !isRef {
			return nil, fmt.Errorf("%s must be an env:NAME reference", key)
		}
		resolved, found := os.LookupEnv(name)
		if !found {
			return nil, fmt.Errorf("environment variable %s is not set", name)
		}
		out[key] = resolved
	}
	return out, nil
}

type headerTransport struct {
	headers map[string]string
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for key, value := range h.headers {
		r.Header.Set(key, value)
	}
	return http.DefaultTransport.RoundTrip(r)
}
