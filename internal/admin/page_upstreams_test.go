package admin_test

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func buildFake(t *testing.T) string {
	t.Helper()
	if prebuilt := os.Getenv("VAULTD_FAKESERVER"); prebuilt != "" {
		return strings.ReplaceAll(prebuilt, `\`, `/`) // for running compiled tests on a machine without Go
	}
	path := filepath.Join(t.TempDir(), "fakeserver")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	output, err := exec.Command("go", "build", "-o", path, "../upstream/testdata/fakeserver").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	return strings.ReplaceAll(path, `\`, `/`)
}

func TestUpstreamPageAndToggles(t *testing.T) {
	f := loggedIn(t, "[[upstreams]]\nname = \"fake\"\ntransport = \"stdio\"\ncommand = \""+buildFake(t)+"\"\nallow = [\"echo\"]\nlocked = [\"dangerous\"]\n")
	_, page := f.get(t, "/admin/upstreams")
	for _, want := range []string{"Connected", "echo", "not_allowed", "dangerous", `aria-label="Locked"`} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(page, `value="dangerous"`) {
		t.Error("a locked tool must have no toggle")
	}

	f.post(t, "/admin/upstreams/tool", url.Values{"name": {"fake"}, "tool": {"pid"}, "on": {"1"}})
	if !slices.Contains(f.store.Get().Upstreams[0].Allow, "pid") {
		t.Errorf("allow: %v", f.store.Get().Upstreams[0].Allow)
	}
	f.post(t, "/admin/upstreams/tool", url.Values{"name": {"fake"}, "tool": {"pid"}})
	if slices.Contains(f.store.Get().Upstreams[0].Allow, "pid") {
		t.Error("pid should be off again")
	}

	// A forged request for a locked tool fails at validation.
	page = f.post(t, "/admin/upstreams/tool", url.Values{"name": {"fake"}, "tool": {"dangerous"}, "on": {"1"}})
	if !strings.Contains(page, `class="error"`) || slices.Contains(f.store.Get().Upstreams[0].Allow, "dangerous") {
		t.Fatalf("locked tool enabled: %s", page)
	}
	if page := f.post(t, "/admin/upstreams/retry", url.Values{"name": {"fake"}}); !strings.Contains(page, "Reconnected fake") {
		t.Errorf("reconnect: %s", page)
	}
}

func TestPortalNeverShowsUpstreamSecrets(t *testing.T) {
	t.Setenv("VAULTD_TEST_SECRET", "s3cret-value")
	f := loggedIn(t, "[[upstreams]]\nname = \"fake\"\ntransport = \"stdio\"\ncommand = \""+buildFake(t)+"\"\nenv = { FAKE_SECRET = \"env:VAULTD_TEST_SECRET\" }\nallow = [\"secret_env\"]\n")
	for _, path := range []string{"/admin/", "/admin/upstreams", "/admin/settings", "/admin/audit", "/admin/apps"} {
		if _, body := f.get(t, path); strings.Contains(body, "s3cret-value") {
			t.Errorf("%s shows the secret", path)
		}
	}
}
