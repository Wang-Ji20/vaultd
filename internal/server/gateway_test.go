package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hortensia/vaultd/internal/audit"
	"github.com/hortensia/vaultd/internal/config"
	"github.com/hortensia/vaultd/internal/index"
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

// newTestStore writes a config for a vault at root. Extra TOML goes at the end.
func newTestStore(t *testing.T, root, extra string) *config.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[server]\nlisten = \"127.0.0.1:0\"\n[vault]\npath = \"" + strings.ReplaceAll(root, `\`, `/`) + "\"\n" +
		"ignore = [\".git\"]\n" + extra
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.OpenStore(path, filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func openTestAudit(t *testing.T) *audit.Logger {
	t.Helper()
	logger, err := audit.Open(t.TempDir(), audit.Settings{MaxSizeMB: 1, MaxFiles: 2, LogArguments: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logger.Close() })
	return logger
}

func newTestGateway(t *testing.T) (*Gateway, string) {
	t.Helper()
	root := t.TempDir()
	writeNote(t, root, "hello.md", "# Hello\nwombats are great\n")
	writeNote(t, root, "sub/other.md", "# Other\nnothing here, see [[hello]]\n")
	writeNote(t, root, ".git/secret.md", "wombats secret\n")
	store := newTestStore(t, root, "")
	if err := store.Update(func(c *config.Config) error {
		c.Vault.WritableFolders = []string{"AI"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(VaultOptions(store.Get()))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Open(filepath.Join(t.TempDir(), "index.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	if err := ix.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	g := New(Options{Store: store, Vault: v, Index: ix, Audit: openTestAudit(t), Version: "test", Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(g.proxy.Close)
	return g, root
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
	if !slices.Equal(list.Folders, []string{"AI", "sub"}) || len(list.Notes) != 1 {
		t.Errorf("list: %+v", list)
	}
	found := callJSON[searchOutput](t, s, "vault.search", map[string]any{"query": "wombats"})
	if len(found.Results) != 1 || found.Results[0].Path != "hello.md" {
		t.Errorf("search: %+v", found)
	}
}

func TestVaultRecentBacklinks(t *testing.T) {
	g, _ := newTestGateway(t)
	s := connect(t, g)
	recent := callJSON[recentOutput](t, s, "vault.recent", map[string]any{"days": 1})
	if len(recent.Notes) != 2 {
		t.Errorf("recent: %+v", recent)
	}
	links := callJSON[backlinksOutput](t, s, "vault.backlinks", map[string]any{"path": "hello.md"})
	if len(links.Backlinks) != 1 || links.Backlinks[0].Path != "sub/other.md" || links.Backlinks[0].Title != "Other" ||
		!strings.Contains(links.Backlinks[0].Context, "[[hello]]") {
		t.Errorf("backlinks: %+v", links)
	}
}

func TestWriteTools(t *testing.T) {
	g, root := newTestGateway(t)
	s := connect(t, g)

	created := callJSON[createOutput](t, s, "vault.create", map[string]any{"path": "AI/new.md", "content": "# New\nfirstword\n"})
	if created.Path != "AI/new.md" {
		t.Errorf("create: %+v", created)
	}
	appended := callJSON[vault.AppendResult](t, s, "vault.append", map[string]any{"path": "AI/new.md", "text": "secondword"})
	if appended.Path != "AI/new.md" || appended.BytesWritten == 0 {
		t.Errorf("append: %+v", appended)
	}
	raw, err := os.ReadFile(filepath.Join(root, "AI", "new.md"))
	if err != nil || !strings.Contains(string(raw), "stdio -->\nsecondword\n") {
		t.Errorf("the app name goes in the marker: %q %v", raw, err)
	}

	// What was just written is searchable at once, with no watcher running.
	found := callJSON[searchOutput](t, s, "vault.search", map[string]any{"query": "secondword"})
	if len(found.Results) != 1 {
		t.Errorf("search after write: %+v", found)
	}

	callJSON[vault.AppendResult](t, s, "memory.remember", map[string]any{"text": "likes wombats"})
	callJSON[vault.AppendResult](t, s, "memory.remember", map[string]any{"text": "likes numbats"})
	recalled := callJSON[recallOutput](t, s, "memory.recall", map[string]any{"query": "numbats"})
	if len(recalled.Lines) != 1 || !strings.HasSuffix(recalled.Lines[0], "(stdio) likes numbats") {
		t.Errorf("recall: %+v", recalled)
	}
}

func TestWriteRefusals(t *testing.T) {
	g, root := newTestGateway(t)
	s := connect(t, g)
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"vault.append", map[string]any{"path": "hello.md", "text": "x"}, "writable"},
		{"vault.create", map[string]any{"path": "elsewhere.md", "content": "x"}, "writable"},
		{"vault.append", map[string]any{"path": "AI/../../x.md", "text": "x"}, "outside the vault"},
		{"vault.create", map[string]any{"path": "AI/.git/x.md", "content": "x"}, "ignored"},
		{"vault.append", map[string]any{"path": "AI/x.txt", "text": "x"}, "only .md"},
		{"memory.recall", map[string]any{"query": " "}, "empty"},
		{"memory.remember", map[string]any{"text": " "}, "empty"},
	}
	for _, tc := range cases {
		text, isErr := call(t, s, tc.tool, tc.args)
		if !isErr || !strings.Contains(text, tc.want) || strings.Contains(text, root) {
			t.Errorf("%s %v: error=%v %q", tc.tool, tc.args, isErr, text)
		}
	}
	callJSON[createOutput](t, s, "vault.create", map[string]any{"path": "AI/once.md", "content": "x"})
	if text, isErr := call(t, s, "vault.create", map[string]any{"path": "AI/once.md", "content": "y"}); !isErr || !strings.Contains(text, "already exists") {
		t.Errorf("overwrite: %v %q", isErr, text)
	}
}

func TestNoDestructiveToolExists(t *testing.T) {
	g, _ := newTestGateway(t)
	res, err := connect(t, g).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		for _, word := range []string{"delete", "remove", "overwrite", "rename", "move", "write"} {
			if strings.Contains(strings.ToLower(tool.Name), word) {
				t.Errorf("tool %s looks destructive", tool.Name)
			}
		}
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
		{"vault.search", map[string]any{"query": "x", "folder": "../.."}, "outside the vault"},
		{"vault.backlinks", map[string]any{"path": "../x.md"}, "outside the vault"},
		{"vault.backlinks", map[string]any{"path": "missing.md"}, "not found"},
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
