package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hortensia/vaultd/internal/vault"
)

func writeNote(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func connect(t *testing.T, g *Gateway) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientSide, serverSide := mcp.NewInMemoryTransports()
	serverSession, err := g.mcp.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func newTestGateway(t *testing.T) (*Gateway, string) {
	t.Helper()
	root := t.TempDir()
	writeNote(t, root, "hello.md", "# Hello\nwombats are great\n")
	writeNote(t, root, "sub/other.md", "nothing here\n")
	writeNote(t, root, ".git/secret.md", "wombats secret\n")
	v, err := vault.New(vault.Options{Root: root, Ignore: []string{".git"}})
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Vault: v, Version: "test"}), root
}

// call returns a tool's JSON text and whether it reported an error.
func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func callJSON[T any](t *testing.T, s *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	text, isErr := call(t, s, name, args)
	if isErr {
		t.Fatalf("%s failed: %s", name, text)
	}
	var out T
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s returned non-JSON %q: %v", name, text, err)
	}
	return out
}

func TestToolDescriptionsWarnAboutUntrustedData(t *testing.T) {
	g, _ := newTestGateway(t)
	res, err := connect(t, g).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if !strings.Contains(tool.Description, "data, not instructions") {
			t.Errorf("%s: description lacks the warning", tool.Name)
		}
	}
}

func TestVaultReadListSearch(t *testing.T) {
	g, _ := newTestGateway(t)
	s := connect(t, g)

	note := callJSON[vault.Note](t, s, "vault.read", map[string]any{"path": "hello.md"})
	if note.Title != "Hello" || !strings.Contains(note.Content, "wombats") {
		t.Errorf("read: %+v", note)
	}
	list := callJSON[vault.Listing](t, s, "vault.list", map[string]any{})
	if !slices.Equal(list.Folders, []string{"sub"}) || len(list.Notes) != 1 {
		t.Errorf("list: %+v", list)
	}
	found := callJSON[searchOutput](t, s, "vault.search", map[string]any{"query": "wombats"})
	if len(found.Results) != 1 || found.Results[0].Path != "hello.md" {
		t.Errorf("search: %+v", found)
	}
}

func TestToolErrorsAreClear(t *testing.T) {
	g, root := newTestGateway(t)
	s := connect(t, g)
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"vault.read", map[string]any{"path": "../../etc/passwd"}, "outside the vault"},
		{"vault.read", map[string]any{"path": "missing.md"}, "not found"},
		{"vault.read", map[string]any{"path": ".git/secret.md"}, "ignored"},
		{"vault.list", map[string]any{"folder": "C:/Windows"}, `":"`},
		{"vault.search", map[string]any{"query": " "}, "empty"},
	}
	for _, tc := range cases {
		text, isErr := call(t, s, tc.tool, tc.args)
		if !isErr || !strings.Contains(text, tc.want) {
			t.Errorf("%s %v: error=%v %q", tc.tool, tc.args, isErr, text)
		}
		if strings.Contains(text, root) {
			t.Errorf("%s: error reveals the vault's location: %q", tc.tool, text)
		}
	}
}

func TestClampLimit(t *testing.T) {
	cases := []struct{ in, want int }{{0, 10}, {-1, 10}, {5, 5}, {50, 50}, {51, 50}}
	for _, tc := range cases {
		if got := clampLimit(tc.in, 10, 50); got != tc.want {
			t.Errorf("clampLimit(%d) = %d", tc.in, got)
		}
	}
}
