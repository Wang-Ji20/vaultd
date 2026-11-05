package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain lets the test binary act as the real program, so tests can start
// vaultd as a separate process without a build step.
func TestMain(m *testing.M) {
	if os.Getenv("VAULTD_TEST_MAIN") == "1" {
		os.Args = append([]string{"vaultd"}, strings.Fields(os.Getenv("VAULTD_TEST_ARGS"))...)
		main()
		os.Exit(0)
	}
	// Keep the index and audit log of every test away from the real data folder.
	home, err := os.MkdirTemp("", "vaultd-test-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	os.Setenv("LOCALAPPDATA", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// selfCommand returns a command that runs vaultd with args.
func selfCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "VAULTD_TEST_MAIN=1", "VAULTD_TEST_ARGS="+strings.Join(args, " "))
	return cmd
}

func writeTestConfig(t *testing.T, vaultDir string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[vault]\npath = \"" + strings.ReplaceAll(vaultDir, `\`, `/`) + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStdioEndToEnd(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "note.md"), []byte("# Note\nwombat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := selfCommand(t, "run", "--stdio", "--config", writeTestConfig(t, vaultDir))
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	waitForIndex(t, session)
	for tool, args := range map[string]map[string]any{
		"vault.list":   {},
		"vault.read":   {"path": "note.md"},
		"vault.search": {"query": "wombat"},
		"vault.recent": {},
	} {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", tool, err, res)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if !json.Valid([]byte(text)) || !strings.Contains(text, "note.md") {
			t.Errorf("%s: %s", tool, text)
		}
	}
}

func waitForIndex(t *testing.T, session *mcp.ClientSession) {
	t.Helper()
	for range 500 {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "vault.search", Arguments: map[string]any{"query": "x"}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Content[0].(*mcp.TextContent).Text, `"indexing":true`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("index never became ready")
}

func TestReindex(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "note.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := dispatch([]string{"reindex", "--config", writeTestConfig(t, vaultDir)}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "notes indexed: 1") {
		t.Fatalf("got %q", stdout.String())
	}
}

// Runs the real program: a token created with the CLI works on the running
// server without a restart, and the server stops cleanly.
func TestHTTPEndToEnd(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "note.md"), []byte("# Note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	body := "[server]\nlisten = \"" + addr + "\"\n[vault]\npath = \"" + strings.ReplaceAll(vaultDir, `\`, `/`) + "\"\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := selfCommand(t, "run", "--config", configPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	defer cmd.Process.Kill()
	waitForHealth(t, addr)

	var stdout, stderr bytes.Buffer
	if err := dispatch([]string{"token", "new", "--config", configPath, "my-app"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(stdout.String())
	if !waitForAccepted(addr, token) {
		t.Fatal("new token was not picked up")
	}
	if saved, _ := os.ReadFile(configPath); strings.Contains(string(saved), token) {
		t.Error("the plain token must never be stored")
	}
	if err := dispatch([]string{"token", "new", "--config", configPath, "my-app"}, &stdout, &stderr); err == nil {
		t.Error("a second token for one app must be refused")
	}

	if runtime.GOOS == "windows" {
		return // this signal does not exist there; the service stop is tested by hand
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("did not stop on SIGTERM")
	}
}

func waitForHealth(t *testing.T, addr string) {
	t.Helper()
	for range 500 {
		if res, err := http.Get("http://" + addr + "/health"); err == nil {
			res.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not start")
}

func waitForAccepted(addr, token string) bool {
	for range 300 {
		req, _ := http.NewRequest("POST", "http://"+addr+"/mcp", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
			if res.StatusCode != http.StatusUnauthorized {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestVersionAndUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := dispatch([]string{"version"}, &stdout, &stderr); err != nil || strings.TrimSpace(stdout.String()) != version {
		t.Errorf("version: %q, %v", stdout.String(), err)
	}
	if err := dispatch([]string{"nope"}, &stdout, &stderr); err == nil {
		t.Error("unknown command must fail")
	}
	if err := dispatch(nil, &stdout, &stderr); err == nil {
		t.Error("no command must fail")
	}
}

func TestRunRefusesBadConfig(t *testing.T) {
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[server]\nlisten = \"0.0.0.0:1\"\n[vault]\npath = \"/\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := dispatch([]string{"run", "--stdio", "--config", path}, &stderr, &stderr)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("got %v", err)
	}
}
