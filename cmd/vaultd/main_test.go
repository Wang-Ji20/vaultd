package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
	os.Exit(m.Run())
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

	for tool, args := range map[string]map[string]any{
		"vault.list":   {},
		"vault.read":   {"path": "note.md"},
		"vault.search": {"query": "wombat"},
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
