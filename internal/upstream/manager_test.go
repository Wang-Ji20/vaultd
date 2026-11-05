package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hortensia/vaultd/internal/config"
)

var ctx = context.Background()

var (
	buildOnce sync.Once
	fakePath  string
	buildErr  error
)

// fakeServer builds the test MCP server once per test run.
func fakeServer(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "vaultd-fake")
		if err != nil {
			buildErr = err
			return
		}
		fakePath = filepath.Join(dir, "fakeserver")
		if runtime.GOOS == "windows" {
			fakePath += ".exe"
		}
		output, err := exec.Command("go", "build", "-o", fakePath, "./testdata/fakeserver").CombinedOutput()
		if err != nil {
			buildErr = errors.New(err.Error() + "\n" + string(output))
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return fakePath
}

func stdioConfig(t *testing.T) config.Upstream {
	t.Helper()
	return config.Upstream{
		Name:      "fake",
		Transport: "stdio",
		Command:   fakeServer(t),
		Allow:     []string{"echo", "fail", "crash", "pid", "secret_env", "missing_tool"},
		Locked:    []string{"dangerous"},
	}
}

func newManager(t *testing.T, upstreams ...config.Upstream) *Manager {
	t.Helper()
	m := NewManager("test", slog.New(slog.DiscardHandler), func() {})
	m.Apply(upstreams)
	t.Cleanup(m.Close)
	return m
}

func args(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func textOf(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	return result.Content[0].(*mcp.TextContent).Text
}

func toolNames(tools []*mcp.Tool) string {
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return strings.Join(names, ",")
}

func TestAllowed(t *testing.T) {
	cfg := config.Upstream{Allow: []string{"a", "both"}, Locked: []string{"both", "l"}}
	cases := map[string]bool{"a": true, "both": false, "l": false, "other": false, "": false}
	for tool, want := range cases {
		if got := Allowed(cfg, tool); got != want {
			t.Errorf("Allowed(%q) = %v", tool, got)
		}
	}
}

func TestSplit(t *testing.T) {
	if up, tool, ok := Split("a.b.c"); !ok || up != "a" || tool != "b.c" {
		t.Errorf("got %q %q %v", up, tool, ok)
	}
	if _, _, ok := Split("plain"); ok {
		t.Error("no dot")
	}
}

// Acceptance test for milestone 5: only allowed tools are visible and callable.
func TestOnlyAllowedToolsAreVisibleAndCallable(t *testing.T) {
	m := newManager(t, stdioConfig(t))
	if got := m.AllowedTools(); len(got) != 0 {
		t.Fatalf("connections must be lazy: %v", toolNames(got))
	}
	if got := m.Status()[0].Status; got != StatusIdle {
		t.Fatalf("status %s", got)
	}
	m.Connect(ctx)
	tools := m.AllowedTools()
	if got := toolNames(tools); got != "fake.crash,fake.echo,fake.fail,fake.pid,fake.secret_env" {
		t.Fatalf("got %s", got)
	}
	if tools[1].Description != "Echo description, unchanged." || tools[1].InputSchema == nil {
		t.Errorf("tool changed: %+v", tools[1])
	}

	result, err := m.Call(ctx, "fake.echo", args(t, map[string]string{"message": "hi"}))
	if err != nil || textOf(t, result) != "echo:hi" {
		t.Fatalf("%+v %v", result, err)
	}
	for _, name := range []string{"fake.dangerous", "fake.not_allowed", "fake.unknown", "fake.missing_tool", "other.echo", "fake", "", ".echo", "fake."} {
		if _, err := m.Call(ctx, name, nil); !errors.Is(err, ErrToolNotAvailable) {
			t.Errorf("%q: got %v", name, err)
		}
	}
}

func TestLockedWinsEvenWhenAlsoAllowed(t *testing.T) {
	cfg := stdioConfig(t)
	cfg.Allow = append(cfg.Allow, "dangerous") // validation forbids this; the proxy must hold anyway
	m := newManager(t, cfg)
	m.Connect(ctx)
	if strings.Contains(toolNames(m.AllowedTools()), "dangerous") {
		t.Error("locked tool visible")
	}
	if _, err := m.Call(ctx, "fake.dangerous", nil); !errors.Is(err, ErrToolNotAvailable) {
		t.Errorf("got %v", err)
	}
}

func TestToolErrorsPassThroughUnchanged(t *testing.T) {
	m := newManager(t, stdioConfig(t))
	result, err := m.Call(ctx, "fake.fail", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || textOf(t, result) != "upstream says no" {
		t.Fatalf("got %+v", result)
	}
}

func TestEnvReferencesReachTheChild(t *testing.T) {
	t.Setenv("VAULTD_TEST_SECRET", "s3cret")
	cfg := stdioConfig(t)
	cfg.Env = map[string]string{"FAKE_SECRET": "env:VAULTD_TEST_SECRET"}
	m := newManager(t, cfg)
	result, err := m.Call(ctx, "fake.secret_env", nil)
	if err != nil || textOf(t, result) != "s3cret" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestMissingEnvNamesVariableOnly(t *testing.T) {
	cfg := stdioConfig(t)
	cfg.Env = map[string]string{"FAKE_SECRET": "env:VAULTD_TEST_UNSET"}
	m := newManager(t, cfg)
	_, err := m.Call(ctx, "fake.echo", nil)
	if err == nil || !strings.Contains(err.Error(), "VAULTD_TEST_UNSET") {
		t.Fatalf("got %v", err)
	}
	info := m.Status()[0]
	if info.Status != StatusError || !strings.Contains(info.Error, "VAULTD_TEST_UNSET") {
		t.Fatalf("got %+v", info)
	}
}

func TestUnavailableUpstreamIsContained(t *testing.T) {
	bad := config.Upstream{Name: "bad", Transport: "stdio", Command: filepath.Join(t.TempDir(), "missing"), Allow: []string{"x"}}
	m := newManager(t, bad, stdioConfig(t))
	m.Connect(ctx)
	if got := toolNames(m.AllowedTools()); !strings.Contains(got, "fake.echo") || strings.Contains(got, "bad.") {
		t.Errorf("got %s", got)
	}
	if _, err := m.Call(ctx, "bad.x", nil); err == nil || errors.Is(err, ErrToolNotAvailable) || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("got %v", err)
	}
	start := time.Now()
	if _, err := m.Call(ctx, "bad.x", nil); err == nil {
		t.Error("want error")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("a dead upstream must answer at once during backoff: %v", elapsed)
	}
	if _, err := m.Call(ctx, "fake.echo", args(t, map[string]string{"message": "x"})); err != nil {
		t.Errorf("the healthy upstream is affected: %v", err)
	}
}

func waitConnected(t *testing.T, m *Manager, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for m.Status()[0].Status != StatusConnected {
		if time.Now().After(deadline) {
			t.Fatalf("not connected within %v: %+v", limit, m.Status()[0])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func pidOf(t *testing.T, m *Manager) int {
	t.Helper()
	result, err := m.Call(ctx, "fake.pid", nil)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(textOf(t, result))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestCrashedChildRestartsWithBackoff(t *testing.T) {
	m := newManager(t, stdioConfig(t))
	first := pidOf(t, m)
	if _, err := m.Call(ctx, "fake.crash", nil); err == nil {
		t.Log("crash call returned without error")
	}
	deadline := time.Now().Add(2 * time.Second)
	for m.Status()[0].Status == StatusConnected {
		if time.Now().After(deadline) {
			t.Fatal("crash not noticed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitConnected(t, m, 3*time.Second) // nobody calls: it restarts by itself
	if second := pidOf(t, m); second == first {
		t.Fatal("not a new process")
	}
}

func TestBackoffGrowsAndHasALimit(t *testing.T) {
	c := newConn(config.Upstream{Name: "x"}, "test", func() {})
	defer c.close()
	var seen []time.Duration
	for range 8 {
		seen = append(seen, c.backoff)
		c.mu.Lock()
		c.failLocked(errors.New("x"))
		c.mu.Unlock()
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i := range want {
		if seen[i] != want[i]*time.Second {
			t.Fatalf("got %v", seen)
		}
	}
}

func TestReconnectGetsANewProcess(t *testing.T) {
	m := newManager(t, stdioConfig(t))
	first := pidOf(t, m)
	if err := m.Reconnect(ctx, "fake"); err != nil {
		t.Fatal(err)
	}
	if second := pidOf(t, m); second == first {
		t.Fatal("same process")
	}
	if err := m.Reconnect(ctx, "nope"); err == nil {
		t.Error("unknown upstream")
	}
}

func processAlive(pid int) bool {
	if runtime.GOOS == "windows" {
		output, _ := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid)).Output()
		return strings.Contains(string(output), strconv.Itoa(pid))
	}
	_, err := os.Stat("/proc/" + strconv.Itoa(pid))
	return err == nil
}

func TestCloseStopsChildren(t *testing.T) {
	m := NewManager("test", slog.New(slog.DiscardHandler), func() {})
	m.Apply([]config.Upstream{stdioConfig(t)})
	pid := pidOf(t, m)
	if !processAlive(pid) {
		t.Skip("cannot observe processes here")
	}
	m.Close()
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatal("child still running")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := m.Call(ctx, "fake.echo", nil); !errors.Is(err, ErrToolNotAvailable) {
		t.Errorf("after close: %v", err)
	}
}

func TestApplyChangesPolicyWithoutRestart(t *testing.T) {
	cfg := stdioConfig(t)
	m := newManager(t, cfg)
	first := pidOf(t, m)

	narrower := cfg
	narrower.Allow = []string{"pid"}
	m.Apply([]config.Upstream{narrower})
	if got := toolNames(m.AllowedTools()); got != "fake.pid" {
		t.Errorf("got %s", got)
	}
	if _, err := m.Call(ctx, "fake.echo", nil); !errors.Is(err, ErrToolNotAvailable) {
		t.Errorf("no longer allowed: %v", err)
	}
	if pidOf(t, m) != first {
		t.Error("a policy change must keep the connection")
	}

	moved := narrower
	moved.Args = []string{}
	moved.Env = map[string]string{"X": "env:PATH"}
	m.Apply([]config.Upstream{moved})
	if pidOf(t, m) == first {
		t.Error("a connection change must restart it")
	}

	m.Apply(nil)
	if len(m.Status()) != 0 {
		t.Error("removed upstreams must go")
	}
}

func TestStatusShowsAllToolsAndLocks(t *testing.T) {
	m := newManager(t, stdioConfig(t))
	before := m.Status()[0]
	if !hasTool(before, "dangerous", true) {
		t.Errorf("locked tools show even before connecting: %+v", before.Tools)
	}
	m.Connect(ctx)
	info := m.Status()[0]
	if info.Status != StatusConnected || info.Transport != "stdio" {
		t.Errorf("got %+v", info)
	}
	if !hasTool(info, "dangerous", true) || !hasTool(info, "not_allowed", false) {
		t.Errorf("got %+v", info.Tools)
	}
	for _, tool := range info.Tools {
		if tool.Name == "echo" && (!tool.Allowed || tool.Description == "") {
			t.Errorf("got %+v", tool)
		}
		if tool.Name == "dangerous" && tool.Allowed {
			t.Errorf("got %+v", tool)
		}
	}
}

func hasTool(info Info, name string, locked bool) bool {
	for _, tool := range info.Tools {
		if tool.Name == name && tool.Locked == locked {
			return true
		}
	}
	return false
}

// A generic HTTP upstream with a header from the environment.
func TestHTTPUpstream(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "http-fake", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "hello", Description: "d"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hello"}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	checked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
	remote := httptest.NewServer(checked)
	t.Cleanup(remote.Close) // after the managers close, or it waits on their open streams

	t.Setenv("VAULTD_TEST_AUTH", "Bearer good")
	cfg := config.Upstream{
		Name: "web", Transport: "http", URL: remote.URL,
		Headers: map[string]string{"Authorization": "env:VAULTD_TEST_AUTH"},
		Allow:   []string{"hello"},
	}
	m := newManager(t, cfg)
	result, err := m.Call(ctx, "web.hello", nil)
	if err != nil || textOf(t, result) != "hello" {
		t.Fatalf("%+v %v", result, err)
	}

	t.Setenv("VAULTD_TEST_AUTH", "Bearer bad")
	refused := newManager(t, cfg)
	if _, err := refused.Call(ctx, "web.hello", nil); err == nil {
		t.Fatal("the wrong header must not connect")
	} else if strings.Contains(err.Error(), "Bearer bad") {
		t.Fatalf("secret in error: %v", err)
	}
}
