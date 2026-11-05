package admin_test

import (
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestAppTokenLifecycle(t *testing.T) {
	f := loggedIn(t, "")
	page := f.post(t, "/admin/apps/new", url.Values{"name": {"cursor"}})
	token := regexp.MustCompile(`vd_[A-Za-z0-9_-]{43}`).FindString(page)
	if token == "" {
		t.Fatalf("token not shown: %s", page)
	}
	if name, ok := f.store.Get().FindApp(token); !ok || name != "cursor" {
		t.Fatal("token does not work")
	}
	saved, err := os.ReadFile(f.store.Path())
	if err != nil || strings.Contains(string(saved), token) {
		t.Fatalf("plain token stored: %v", err)
	}
	for _, client := range []string{"Claude Desktop", "Cursor", "Claude Code", "DeepSeek Harness", "@deepseek-ai/dsh-mcp-client", "streamable-http"} {
		if !strings.Contains(page, client) {
			t.Errorf("snippet missing %q", client)
		}
	}
	if _, again := f.get(t, "/admin/apps"); strings.Contains(again, token) {
		t.Fatal("token shown a second time")
	}
	if page := f.post(t, "/admin/apps/new", url.Values{"name": {"cursor"}}); !strings.Contains(page, "already exists") || regexp.MustCompile(`id="new-token"`).MatchString(page) {
		t.Errorf("duplicate: %s", page)
	}

	page = f.post(t, "/admin/apps/rotate", url.Values{"name": {"cursor"}})
	rotated := regexp.MustCompile(`vd_[A-Za-z0-9_-]{43}`).FindString(page)
	if rotated == "" || rotated == token {
		t.Fatal("rotate gave no new token")
	}
	if _, ok := f.store.Get().FindApp(token); ok {
		t.Error("old token still works")
	}
	if _, ok := f.store.Get().FindApp(rotated); !ok {
		t.Error("new token does not work")
	}

	f.post(t, "/admin/apps/revoke", url.Values{"name": {"cursor"}})
	if _, ok := f.store.Get().FindApp(rotated); ok {
		t.Error("revoked token still works")
	}
}
