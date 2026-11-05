package server

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hortensia/vaultd/internal/audit"
	"github.com/hortensia/vaultd/internal/config"
	"github.com/hortensia/vaultd/internal/index"
	"github.com/hortensia/vaultd/internal/vault"
)

var (
	buildOnce sync.Once
	fakePath  string
	buildErr  error
)

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
		output, err := exec.Command("go", "build", "-o", fakePath, "../upstream/testdata/fakeserver").CombinedOutput()
		if err != nil {
			t.Log(string(output))
			buildErr = err
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return fakePath
}

type proxyFixture struct {
	gateway  *Gateway
	store    *config.Store
	auditDir string
}

func newProxyGateway(t *testing.T) *proxyFixture {
	t.Helper()
	root := t.TempDir()
	writeNote(t, root, "hello.md", "# Hello\n")
	command := strings.ReplaceAll(fakeServer(t), `\`, `/`)
	store := newTestStore(t, root, "[[upstreams]]\nname = \"fake\"\ntransport = \"stdio\"\ncommand = \""+command+"\"\n"+
		"allow = [\"echo\", \"fail\"]\nlocked = [\"dangerous\"]\n"+
		"[[upstreams]]\nname = \"dead\"\ntransport = \"stdio\"\ncommand = \"/does/not/exist\"\nallow = [\"x\"]\n")
	v, err := vault.New(VaultOptions(store.Get()))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Open(filepath.Join(t.TempDir(), "index.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	auditDir := t.TempDir()
	log, err := audit.Open(auditDir, audit.Settings{MaxSizeMB: 1, MaxFiles: 2, LogArguments: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	g := New(Options{Store: store, Vault: v, Index: ix, Audit: log, Version: "test", Logger: slog.New(slog.DiscardHandler)})
	ctx, cancel := context.WithCancel(context.Background())
	if err := g.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); g.Wait() })
	return &proxyFixture{gateway: g, store: store, auditDir: auditDir}
}

func listNames(t *testing.T, s *mcp.ClientSession) []string {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestProxyFiltersTools(t *testing.T) {
	f := newProxyGateway(t)
	s := connect(t, f.gateway)
	names := listNames(t, s)
	for _, want := range []string{"vault.read", "memory.recall", "fake.echo", "fake.fail"} {
		if !slices.Contains(names, want) {
			t.Errorf("missing %s in %v", want, names)
		}
	}
	for _, hidden := range []string{"fake.dangerous", "fake.not_allowed", "fake.crash", "dead.x"} {
		if slices.Contains(names, hidden) {
			t.Errorf("visible: %s", hidden)
		}
	}
}

func TestProxyPassesDescriptionAndSchemaThrough(t *testing.T) {
	f := newProxyGateway(t)
	res, err := connect(t, f.gateway).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "fake.echo" {
			continue
		}
		if tool.Description != "Echo description, unchanged." {
			t.Errorf("description %q", tool.Description)
		}
		schema, _ := json.Marshal(tool.InputSchema)
		if !strings.Contains(string(schema), `"message"`) {
			t.Errorf("schema %s", schema)
		}
		return
	}
	t.Fatal("fake.echo not found")
}

func TestProxyCalls(t *testing.T) {
	f := newProxyGateway(t)
	s := connect(t, f.gateway)
	// Works even before any tool list, because connections are lazy.
	text, isErr := call(t, s, "fake.echo", map[string]any{"message": "hi"})
	if isErr || text != "echo:hi" {
		t.Fatalf("%v %q", isErr, text)
	}
	if text, isErr := call(t, s, "fake.fail", nil); !isErr || text != "upstream says no" {
		t.Errorf("tool error changed: %v %q", isErr, text)
	}
	for _, name := range []string{"fake.dangerous", "fake.not_allowed", "fake.nope", "nobody.x", "plain", "vault.delete"} {
		text, isErr := call(t, s, name, nil)
		if !isErr || text != "tool not available" {
			t.Errorf("%s: %v %q", name, isErr, text)
		}
	}
}

func TestDeadUpstreamLeavesVaultWorking(t *testing.T) {
	f := newProxyGateway(t)
	s := connect(t, f.gateway)
	if text, isErr := call(t, s, "dead.x", nil); !isErr || !strings.Contains(text, "unavailable") {
		t.Errorf("%v %q", isErr, text)
	}
	if _, isErr := call(t, s, "vault.read", map[string]any{"path": "hello.md"}); isErr {
		t.Error("vault tools must keep working")
	}
}

func TestAllowListHotReload(t *testing.T) {
	f := newProxyGateway(t)
	changed := make(chan struct{}, 10)
	clientSide, serverSide := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := f.gateway.mcp.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
	})
	s, err := client.Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !slices.Contains(listNames(t, s), "fake.fail") {
		t.Fatal("fake.fail missing")
	}
	for len(changed) > 0 {
		<-changed
	}

	err = f.store.Update(func(c *config.Config) error {
		c.Upstreams[0].Allow = []string{"echo"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Error("apps must be told the list changed")
	}
	if slices.Contains(listNames(t, s), "fake.fail") {
		t.Error("no longer allowed, still listed")
	}
	if text, isErr := call(t, s, "fake.fail", nil); !isErr || text != "tool not available" {
		t.Errorf("no longer allowed, still callable: %q", text)
	}
}

func TestBadUpstreamSchemaCannotCrashGateway(t *testing.T) {
	f := newProxyGateway(t)
	bad := &mcp.Tool{Name: "fake.bad", InputSchema: map[string]any{"type": "string"}}
	if f.gateway.addUpstreamTool(bad) {
		t.Fatal("the SDK should refuse this schema")
	}
}

func readAudit(t *testing.T, dir string) []audit.Entry {
	t.Helper()
	file, err := os.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out []audit.Entry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry audit.Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		out = append(out, entry)
	}
	return out
}

// Acceptance test for milestone 5: every call is in the audit log.
func TestEveryCallIsAudited(t *testing.T) {
	f := newProxyGateway(t)
	s := connect(t, f.gateway)
	call(t, s, "vault.read", map[string]any{"path": "hello.md"})
	call(t, s, "vault.read", map[string]any{"path": "../secret.md"})
	call(t, s, "fake.echo", map[string]any{"message": "hi"})
	call(t, s, "fake.fail", nil)
	call(t, s, "fake.dangerous", nil)
	entries := readAudit(t, f.auditDir)
	want := []struct {
		tool string
		ok   bool
	}{{"vault.read", true}, {"vault.read", false}, {"fake.echo", true}, {"fake.fail", false}, {"fake.dangerous", false}}
	if len(entries) != len(want) {
		t.Fatalf("got %+v", entries)
	}
	for i, w := range want {
		got := entries[i]
		if got.Tool != w.tool || got.OK != w.ok || got.App != StdioApp || got.TS == "" {
			t.Errorf("entry %d: %+v", i, got)
		}
		if w.ok && got.ResultBytes == 0 {
			t.Errorf("entry %d lacks a size: %+v", i, got)
		}
		if !w.ok && got.Error == "" {
			t.Errorf("entry %d lacks an error: %+v", i, got)
		}
	}
	if entries[0].Args.(map[string]any)["path"] != "hello.md" {
		t.Errorf("args %+v", entries[0].Args)
	}
	if entries[4].Error != "tool not available" {
		t.Errorf("got %+v", entries[4])
	}
}

func TestAuditRecordsAppNotTokenOverHTTP(t *testing.T) {
	srv, tokens := startServer(t)
	dir := t.TempDir()
	logger, err := audit.Open(dir, audit.Settings{MaxSizeMB: 1, MaxFiles: 2, LogArguments: true})
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	srv.gateway.audit = logger
	session := connectHTTP(t, srv.addr, tokens["app-b"])
	call(t, session, "vault.read", map[string]any{"path": "hello.md"})
	entries := readAudit(t, dir)
	if len(entries) != 1 || entries[0].App != "app-b" {
		t.Fatalf("got %+v", entries)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if strings.Contains(string(raw), tokens["app-b"]) || strings.Contains(string(raw), "Bearer") {
		t.Fatalf("token in the log: %s", raw)
	}
}

func TestAuditSettingsHotReload(t *testing.T) {
	f := newProxyGateway(t)
	s := connect(t, f.gateway)
	err := f.store.Update(func(c *config.Config) error {
		c.Audit.LogArguments = false
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	call(t, s, "vault.read", map[string]any{"path": "hello.md"})
	entries := readAudit(t, f.auditDir)
	if entries[0].Args != nil || entries[0].ArgsBytes == 0 {
		t.Fatalf("got %+v", entries[0])
	}
}
