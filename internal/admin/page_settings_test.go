package admin_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSettings(t *testing.T) {
	f := loggedIn(t, "")
	form := url.Values{"max_files": {"7"}, "max_size_mb": {"3"}, "session_ttl": {"2h"}, "theme": {"dark"}}
	page := f.post(t, "/admin/settings", form)
	cfg := f.store.Get()
	if cfg.Audit.MaxFiles != 7 || cfg.Audit.MaxSizeMB != 3 || cfg.Audit.LogArguments || cfg.Admin.SessionTTL != 2*time.Hour || cfg.Admin.Theme != "dark" {
		t.Errorf("got %+v", cfg)
	}
	if !strings.Contains(page, `<html lang="en" data-theme="dark">`) {
		t.Error("theme not applied")
	}
	for name, bad := range map[string]url.Values{
		"size":  {"max_files": {"7"}, "max_size_mb": {"x"}, "session_ttl": {"2h"}, "theme": {"dark"}},
		"ttl":   {"max_files": {"7"}, "max_size_mb": {"3"}, "session_ttl": {"soon"}, "theme": {"dark"}},
		"zero":  {"max_files": {"0"}, "max_size_mb": {"3"}, "session_ttl": {"2h"}, "theme": {"dark"}},
		"theme": {"max_files": {"7"}, "max_size_mb": {"3"}, "session_ttl": {"2h"}, "theme": {"pink"}},
	} {
		if page := f.post(t, "/admin/settings", bad); !strings.Contains(page, `class="error"`) {
			t.Errorf("%s: no inline error", name)
		}
	}
	if f.store.Get().Admin.Theme != "dark" || f.store.Get().Audit.MaxFiles != 7 {
		t.Error("a bad form changed something")
	}
}

func TestPagesRequireLogin(t *testing.T) {
	f := startWithPassword(t, "")
	f.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, path := range []string{"/admin/", "/admin/apps", "/admin/vault", "/admin/upstreams", "/admin/audit", "/admin/settings"} {
		res, _ := f.get(t, path)
		if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/login" {
			t.Errorf("%s: %d %s", path, res.StatusCode, res.Header.Get("Location"))
		}
	}
}
