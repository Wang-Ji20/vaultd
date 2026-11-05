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
	"strconv"
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

func fakeServerPath(t *testing.T) string {
	t.Helper()
	if prebuilt := os.Getenv("VAULTD_FAKESERVER"); prebuilt != "" {
		return prebuilt // for running compiled tests on a machine without Go
	}
	path := filepath.Join(t.TempDir(), "fakeserver")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	output, err := exec.Command("go", "build", "-o", path, "../../internal/upstream/testdata/fakeserver").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	return path
}

func processAlive(pid int) bool {
	if runtime.GOOS == "windows" {
		output, _ := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid)).Output()
		return strings.Contains(string(output), strconv.Itoa(pid))
	}
	_, err := os.Stat("/proc/" + strconv.Itoa(pid))
	return err == nil
}

// Killing vaultd outright gives it no chance to stop its children. They must
// die anyway: by a job object on Windows, by their input closing elsewhere.
func TestUpstreamChildrenDieWithVaultd(t *testing.T) {
	fake := strings.ReplaceAll(fakeServerPath(t), `\`, `/`)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	body := "[vault]\npath = \"" + strings.ReplaceAll(t.TempDir(), `\`, `/`) + "\"\n" +
		"[[upstreams]]\nname = \"fake\"\ntransport = \"stdio\"\ncommand = \"" + fake + "\"\nallow = [\"pid\"]\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := selfCommand(t, "run", "--stdio", "--config", configPath)
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	transport := &mcp.CommandTransport{Command: cmd}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "fake.pid"})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	pid, err := strconv.Atoi(res.Content[0].(*mcp.TextContent).Text)
	if err != nil {
		t.Fatal(err)
	}
	if !processAlive(pid) {
		t.Skip("cannot observe processes here")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatal("the child outlived vaultd")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHashPasswordWritesConfig(t *testing.T) {
	configPath := writeTestConfig(t, t.TempDir())
	input, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.WriteString("my password\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := hashPasswordCommand([]string{"--config", configPath, "--write"}, input, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "my password") || !strings.Contains(string(saved), "$2a$") {
		t.Fatalf("got %s", saved)
	}
	if _, err := hashPassword(""); err == nil {
		t.Error("empty password")
	}
	if _, err := hashPassword(strings.Repeat("x", 73)); err == nil {
		t.Error("bcrypt's limit must give a clear error")
	}
}

func TestDataDirFlagIsUsed(t *testing.T) {
	vaultDir, dataDir := t.TempDir(), t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"reindex", "--config", writeTestConfig(t, vaultDir), "--data-dir", dataDir}
	if err := dispatch(args, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "index.db")); err != nil {
		t.Fatal(err)
	}
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
