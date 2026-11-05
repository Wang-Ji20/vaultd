package admin_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/hortensia/vaultd/internal/admin"
	"github.com/hortensia/vaultd/internal/audit"
	"github.com/hortensia/vaultd/internal/config"
	"github.com/hortensia/vaultd/internal/index"
	"github.com/hortensia/vaultd/internal/server"
	"github.com/hortensia/vaultd/internal/vault"
)

const password = "correct horse"

var (
	hashOnce sync.Once
	hash     string
)

func passwordHash(t *testing.T) string {
	t.Helper()
	hashOnce.Do(func() {
		raw, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		hash = string(raw)
	})
	return hash
}

type fixture struct {
	url     string
	store   *config.Store
	gateway *server.Gateway
	root    string
	client  *http.Client
}

func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func start(t *testing.T, passwordLine, extra string) *fixture {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("# Note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[vault]\npath = \"" + strings.ReplaceAll(root, `\`, `/`) + "\"\nwritable_folders = [\"AI\"]\nignore = [\".git\"]\n" +
		"[admin]\nsession_ttl = \"1h\"\n" + passwordLine + "\n" + extra
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.OpenStore(path, filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(server.VaultOptions(store.Get()))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Open(filepath.Join(t.TempDir(), "index.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	log, err := audit.Open(t.TempDir(), server.AuditSettings(store.Get()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	gateway := server.New(server.Options{Store: store, Vault: v, Index: ix, Audit: log, Version: "test-1", Logger: slog.New(slog.DiscardHandler)})
	ctx, cancel := context.WithCancel(context.Background())
	portal, err := admin.New(admin.Options{Store: store, Backend: gateway, Version: "test-1", Base: ctx})
	if err != nil {
		t.Fatal(err)
	}
	gateway.MountAdmin(portal)
	if err := gateway.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); gateway.Wait() })

	srv := httptest.NewUnstartedServer(nil)
	srv.Config.Handler = gateway.Handler(srv.Listener.Addr().String())
	srv.Start()
	t.Cleanup(srv.Close)
	return &fixture{url: srv.URL, store: store, gateway: gateway, root: root, client: newClient(t)}
}

func startWithPassword(t *testing.T, extra string) *fixture {
	t.Helper()
	return start(t, fmt.Sprintf("password_hash = %q", passwordHash(t)), extra)
}

func (f *fixture) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	res, err := f.client.Get(f.url + path)
	if err != nil {
		t.Fatal(err)
	}
	return res, readBody(t, res)
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func csrfIn(t *testing.T, body string) string {
	t.Helper()
	m := csrfRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no CSRF field in %s", body)
	}
	return m[1]
}

func (f *fixture) login(t *testing.T, pass string) (*http.Response, string) {
	t.Helper()
	_, page := f.get(t, "/admin/login")
	res, err := f.client.PostForm(f.url+"/admin/login", url.Values{"csrf": {csrfIn(t, page)}, "password": {pass}})
	if err != nil {
		t.Fatal(err)
	}
	return res, readBody(t, res)
}

func loggedIn(t *testing.T, extra string) *fixture {
	t.Helper()
	f := startWithPassword(t, extra)
	res, body := f.login(t, password)
	if res.StatusCode != 200 || !strings.Contains(body, "Overview") {
		t.Fatalf("login: %d %s", res.StatusCode, body)
	}
	return f
}

// post sends a form with the page's CSRF token and returns the new page.
func (f *fixture) post(t *testing.T, path string, form url.Values) string {
	t.Helper()
	_, page := f.get(t, "/admin/")
	form.Set("csrf", csrfIn(t, page))
	res, err := f.client.PostForm(f.url+path, form)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, res)
	if res.StatusCode != 200 {
		t.Fatalf("POST %s: %d %s", path, res.StatusCode, body)
	}
	return body
}

func TestNoPasswordShowsOnlySetupPage(t *testing.T) {
	f := start(t, "", "")
	for _, path := range []string{"/admin/", "/admin/login"} {
		res, body := f.get(t, path)
		if res.StatusCode != 200 || !strings.Contains(body, "vaultd hash-password") || strings.Contains(body, "<nav") {
			t.Errorf("%s: %d %s", path, res.StatusCode, body)
		}
	}
	before := f.store.Get()
	res, err := f.client.PostForm(f.url+"/admin/restore", url.Values{"name": {"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, res); !strings.Contains(body, "vaultd hash-password") || f.store.Get() != before {
		t.Error("no change may pass the setup page")
	}
}

func TestLoginAndSessionCookie(t *testing.T) {
	f := startWithPassword(t, "")
	f.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, _ := f.login(t, "wrong")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", res.StatusCode)
	}
	res, _ = f.login(t, password)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("right password: %d", res.StatusCode)
	}
	var found *http.Cookie
	for _, cookie := range res.Cookies() {
		if cookie.Name == "vaultd_session" {
			found = cookie
		}
	}
	if found == nil {
		t.Fatal("no session cookie")
	}
	if !found.HttpOnly || found.SameSite != http.SameSiteStrictMode || found.Path != "/admin" || found.Expires.IsZero() {
		t.Errorf("cookie %+v", found)
	}
	if time.Until(found.Expires) > time.Hour+time.Minute {
		t.Errorf("expires %v, session_ttl is 1h", found.Expires)
	}
	if len(found.Value) != 43 { // 32 random bytes in base64
		t.Errorf("value length %d", len(found.Value))
	}
	if res, _ := f.get(t, "/admin/"); res.StatusCode != 200 {
		t.Errorf("after login: %d", res.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	f := startWithPassword(t, "")
	for i := range 5 {
		if res, _ := f.login(t, "wrong"); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, res.StatusCode)
		}
	}
	res, body := f.login(t, password)
	if res.StatusCode != http.StatusTooManyRequests || !strings.Contains(body, "Too many") {
		t.Fatalf("even the right password must wait: %d", res.StatusCode)
	}
}

func TestLoginRequiresCSRF(t *testing.T) {
	f := startWithPassword(t, "")
	f.get(t, "/admin/login")
	res, err := f.client.PostForm(f.url+"/admin/login", url.Values{"csrf": {"forged"}, "password": {password}})
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, res)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d", res.StatusCode)
	}
}

func TestCSRFRejection(t *testing.T) {
	f := loggedIn(t, "")
	before := f.store.Get()
	for _, form := range []url.Values{
		{"name": {"x"}},
		{"name": {"x"}, "csrf": {"forged"}},
	} {
		res, err := f.client.PostForm(f.url+"/admin/restore", form)
		if err != nil {
			t.Fatal(err)
		}
		readBody(t, res)
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("got %d", res.StatusCode)
		}
	}
	if f.store.Get() != before {
		t.Fatal("a rejected request changed the config")
	}
}

func TestCSRFHeaderWorksForHtmx(t *testing.T) {
	f := loggedIn(t, "")
	_, page := f.get(t, "/admin/")
	token := csrfIn(t, page)
	if !strings.Contains(page, `hx-headers='{"X-CSRF-Token":"`+token+`"}'`) {
		t.Fatalf("no hx-headers in page")
	}
	req, err := http.NewRequest("POST", f.url+"/admin/restore", strings.NewReader("name=nope"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", token)
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, res)
	if res.StatusCode != 200 {
		t.Fatalf("got %d", res.StatusCode)
	}
}

// Admin routes never accept bearer tokens.
func TestBearerTokenOpensNothing(t *testing.T) {
	token, tokenHash := config.NewToken()
	f := startWithPassword(t, fmt.Sprintf("[[apps]]\nname = \"a\"\ntoken_sha256 = %q\n", tokenHash))
	f.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequest("GET", f.url+"/admin/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, res)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("got %d", res.StatusCode)
	}
}

func TestSecurityHeaders(t *testing.T) {
	f := startWithPassword(t, "")
	for _, path := range []string{"/admin/login", "/admin/static/app.css", "/admin/"} {
		res, _ := f.get(t, path)
		if got := res.Header.Get("Content-Security-Policy"); got != "default-src 'self'" {
			t.Errorf("%s CSP: %q", path, got)
		}
		if res.Header.Get("X-Frame-Options") != "DENY" || res.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: %v", path, res.Header)
		}
	}
}

func TestHostCheckCoversAdmin(t *testing.T) {
	f := startWithPassword(t, "")
	req, err := http.NewRequest("GET", f.url+"/admin/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example"
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, res)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d", res.StatusCode)
	}
}

func TestLogoutEndsSession(t *testing.T) {
	f := loggedIn(t, "")
	f.post(t, "/admin/logout", url.Values{})
	if _, body := f.get(t, "/admin/"); !strings.Contains(body, "Admin password") {
		t.Fatalf("still logged in: %s", body)
	}
}

func TestPasswordChangeEndsSessions(t *testing.T) {
	f := loggedIn(t, "")
	err := f.store.Update(func(c *config.Config) error {
		c.Admin.PasswordHash = "$2a$04$changedchangedchangedchangedchangedchangedchangedchang"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, body := f.get(t, "/admin/"); !strings.Contains(body, "Admin password") {
		t.Fatal("old session survived a password change")
	}
}

func TestHtmxGetsRedirectHeaderWhenLoggedOut(t *testing.T) {
	f := startWithPassword(t, "")
	req, err := http.NewRequest("GET", f.url+"/admin/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("HX-Request", "true")
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, res)
	if res.Header.Get("HX-Redirect") != "/admin/login" {
		t.Fatalf("got %v", res.Header)
	}
}

func TestOverview(t *testing.T) {
	f := loggedIn(t, "")
	if err := f.gateway.AuditLog().Record(audit.Call{App: "stdio", Tool: "vault.read"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, page := f.get(t, "/admin/")
	configuredPath := strings.ReplaceAll(f.root, `\`, `/`) // as the test wrote it in the config
	for _, want := range []string{"test-1", configuredPath, "vault.read", `hx-trigger="every 5s"`, "No upstreams"} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestRestoreBackup(t *testing.T) {
	f := loggedIn(t, "")
	err := f.store.Update(func(c *config.Config) error {
		c.Vault.GitAutocommit = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	backups, err := f.store.Backups()
	if err != nil || len(backups) != 1 {
		t.Fatalf("%v %v", backups, err)
	}
	if _, page := f.get(t, "/admin/"); !strings.Contains(page, backups[0]) {
		t.Error("backup not offered")
	}
	f.post(t, "/admin/restore", url.Values{"name": {backups[0]}})
	if f.store.Get().Vault.GitAutocommit {
		t.Error("not restored")
	}
	if page := f.post(t, "/admin/restore", url.Values{"name": {"../../etc/passwd"}}); !strings.Contains(page, `class="error"`) {
		t.Error("a bad name must be refused")
	}
}

func TestStaticFiles(t *testing.T) {
	f := startWithPassword(t, "")
	for _, path := range []string{"app.css", "app.js", "htmx.min.js", "inter.woff2", "LICENSE-htmx.txt", "LICENSE-inter.txt"} {
		if res, body := f.get(t, "/admin/static/"+path); res.StatusCode != 200 || body == "" {
			t.Errorf("%s: %d", path, res.StatusCode)
		}
	}
	if res, _ := f.get(t, "/admin/static/../../config.toml"); res.StatusCode == 200 {
		t.Error("path traversal")
	}
}
