package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hortensia/vaultd/internal/config"
	"github.com/hortensia/vaultd/internal/index"
	"github.com/hortensia/vaultd/internal/vault"
)

const testVersion = "test-1"

type testServer struct {
	addr    string
	gateway *Gateway
	store   *config.Store
	root    string
	stop    func()
}

// startServer runs a real server on a random loopback port with two apps.
func startServer(t *testing.T) (*testServer, map[string]string) {
	t.Helper()
	root := t.TempDir()
	writeNote(t, root, "hello.md", "# Hello\nwombats\n")
	tokens := map[string]string{}
	apps := ""
	for _, name := range []string{"app-a", "app-b"} {
		token, hash := config.NewToken()
		tokens[name] = token
		apps += fmt.Sprintf("[[apps]]\nname = %q\ntoken_sha256 = %q\n", name, hash)
	}
	store := newTestStore(t, root, apps)
	v, err := vault.New(VaultOptions(store.Get()))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Open(filepath.Join(t.TempDir(), "index.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	gateway := New(Options{Store: store, Vault: v, Index: ix, Version: testVersion})
	ctx, cancel := context.WithCancel(context.Background())
	if err := gateway.Start(ctx); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	served := make(chan error, 1)
	go func() { served <- gateway.Serve(ctx, ready) }()
	addr := <-ready
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			if err := <-served; err != nil {
				t.Errorf("serve: %v", err)
			}
			gateway.Wait()
			ix.Close()
		})
	}
	t.Cleanup(stop)
	return &testServer{addr: addr, gateway: gateway, store: store, root: root, stop: stop}, tokens
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func connectHTTP(t *testing.T, addr, token string) *mcp.ClientSession {
	t.Helper()
	transport := &mcp.StreamableClientTransport{
		Endpoint:   "http://" + addr + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token}},
		MaxRetries: -1,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func request(t *testing.T, method, url, host string, header map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for key, value := range header {
		req.Header.Set(key, value)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func TestHealthNeedsNoToken(t *testing.T) {
	srv, _ := startServer(t)
	code, body := request(t, "GET", "http://"+srv.addr+"/health", "", nil)
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil || code != 200 {
		t.Fatalf("%d %q", code, body)
	}
	if got["status"] != "ok" || got["version"] != testVersion {
		t.Fatalf("got %v", got)
	}
}

func TestMCPRequiresValidBearer(t *testing.T) {
	srv, tokens := startServer(t)
	url := "http://" + srv.addr + "/mcp"
	cases := map[string]map[string]string{
		"none":           nil,
		"wrong":          {"Authorization": "Bearer vd_wrong"},
		"hash as token":  {"Authorization": "Bearer " + config.HashToken(tokens["app-a"])},
		"basic scheme":   {"Authorization": "Basic " + tokens["app-a"]},
		"no scheme":      {"Authorization": tokens["app-a"]},
		"session cookie": {"Cookie": "vaultd_session=" + tokens["app-a"]},
	}
	for name, header := range cases {
		if code, _ := request(t, "POST", url, "", header); code != http.StatusUnauthorized {
			t.Errorf("%s: got %d", name, code)
		}
	}
	if code, _ := request(t, "POST", url, "", map[string]string{"Authorization": "Bearer " + tokens["app-a"]}); code == http.StatusUnauthorized {
		t.Error("a valid token was refused")
	}
}

func TestHostHeaderChecked(t *testing.T) {
	srv, tokens := startServer(t)
	_, port, _ := strings.Cut(srv.addr, ":")
	bad := []string{"evil.example", "evil.example:" + port, "localhost:" + port, "127.0.0.1:1", "127.0.0.1"}
	auth := map[string]string{"Authorization": "Bearer " + tokens["app-a"]}
	for _, host := range bad {
		for _, route := range []string{"/health", "/mcp", "/anything"} {
			if code, _ := request(t, "POST", "http://"+srv.addr+route, host, auth); code != http.StatusForbidden {
				t.Errorf("host %q route %s: got %d", host, route, code)
			}
		}
	}
}

// Acceptance test for milestone 3: two clients at once, each its own app.
func TestTwoAppsShareTheVault(t *testing.T) {
	srv, tokens := startServer(t)
	a := connectHTTP(t, srv.addr, tokens["app-a"])
	b := connectHTTP(t, srv.addr, tokens["app-b"])
	var wg sync.WaitGroup
	for _, session := range []*mcp.ClientSession{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				note := callJSON[vault.Note](t, session, "vault.read", map[string]any{"path": "hello.md"})
				if note.Title != "Hello" {
					t.Errorf("got %+v", note)
				}
			}
		}()
	}
	wg.Wait()
}

func TestAppNameFromToken(t *testing.T) {
	srv, tokens := startServer(t)
	seen := make(chan string, 1)
	addTool(srv.gateway, "test.whoami", "test", func(_ context.Context, app string, _ struct{}) (string, error) {
		seen <- app
		return app, nil
	})
	call(t, connectHTTP(t, srv.addr, tokens["app-b"]), "test.whoami", nil)
	if got := <-seen; got != "app-b" {
		t.Fatalf("got %q", got)
	}
}

func TestOneAppCannotUseAnothersSession(t *testing.T) {
	srv, tokens := startServer(t)
	session := connectHTTP(t, srv.addr, tokens["app-a"])
	code, _ := request(t, "POST", "http://"+srv.addr+"/mcp", "", map[string]string{
		"Authorization":  "Bearer " + tokens["app-b"],
		"Mcp-Session-Id": session.ID(),
	})
	if code != http.StatusForbidden {
		t.Fatalf("got %d", code)
	}
}

func TestTokenChangesApplyWithoutRestart(t *testing.T) {
	srv, tokens := startServer(t)
	url := "http://" + srv.addr + "/mcp"
	newToken, hash := config.NewToken()
	err := srv.store.Update(func(c *config.Config) error {
		c.Apps[0].TokenSHA256 = hash
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := request(t, "POST", url, "", map[string]string{"Authorization": "Bearer " + tokens["app-a"]}); code != http.StatusUnauthorized {
		t.Errorf("revoked token: got %d", code)
	}
	if code, _ := request(t, "POST", url, "", map[string]string{"Authorization": "Bearer " + newToken}); code == http.StatusUnauthorized {
		t.Error("new token refused")
	}
}

func TestVaultSettingsHotReload(t *testing.T) {
	srv, tokens := startServer(t)
	session := connectHTTP(t, srv.addr, tokens["app-a"])
	if _, isErr := call(t, session, "vault.read", map[string]any{"path": "hello.md"}); isErr {
		t.Fatal("read failed")
	}

	other := t.TempDir()
	writeNote(t, other, "elsewhere.md", "# Elsewhere\nnumbat\n")
	err := srv.store.Update(func(c *config.Config) error {
		c.Vault.Path = other
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, isErr := call(t, session, "vault.read", map[string]any{"path": "elsewhere.md"}); isErr {
		t.Error("new vault not active")
	}
	if text, isErr := call(t, session, "vault.read", map[string]any{"path": "hello.md"}); !isErr {
		t.Errorf("old vault still served: %s", text)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out := callJSON[searchOutput](t, session, "vault.search", map[string]any{"query": "numbat"})
		if len(out.Results) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("index not moved to the new vault: %+v", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if out := callJSON[searchOutput](t, session, "vault.search", map[string]any{"query": "wombats"}); len(out.Results) != 0 {
		t.Errorf("old vault still searchable: %+v", out)
	}
}

func TestShutdownFinishesRunningCalls(t *testing.T) {
	srv, tokens := startServer(t)
	started := make(chan struct{})
	release := make(chan struct{})
	addTool(srv.gateway, "test.slow", "test", func(context.Context, string, struct{}) (string, error) {
		close(started)
		<-release
		return "finished", nil
	})
	session := connectHTTP(t, srv.addr, tokens["app-a"])
	result := make(chan string, 1)
	go func() {
		text, _ := call(t, session, "test.slow", nil)
		result <- text
	}()
	<-started
	stopped := make(chan struct{})
	go func() { srv.stop(); close(stopped) }()
	deadline := time.Now().Add(time.Second)
	for !srv.gateway.draining.Load() {
		if time.Now().After(deadline) {
			t.Fatal("never started draining")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code, _ := request(t, "GET", "http://"+srv.addr+"/health", "", nil); code != http.StatusServiceUnavailable {
		t.Errorf("new request while draining: got %d", code)
	}
	close(release)
	if got := <-result; !strings.Contains(got, "finished") {
		t.Errorf("got %q", got)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown is waiting for open streams")
	}
}

func TestListenLoopbackRefusesOthers(t *testing.T) {
	if l, err := listenLoopback("0.0.0.0:0"); err == nil {
		l.Close()
		t.Fatal("must refuse all interfaces")
	}
	l, err := listenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := allowedHostFor("127.0.0.1:0", l.Addr())
	if strings.HasSuffix(got, ":0") || !strings.HasPrefix(got, "127.0.0.1:") {
		t.Fatalf("got %q", got)
	}
}
