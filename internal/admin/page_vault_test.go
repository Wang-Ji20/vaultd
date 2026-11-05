package admin_test

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/hortensia/vaultd/internal/config"
	"github.com/hortensia/vaultd/internal/vault"
)

// Spec §15: toggling a writable folder updates the config and hot-reloads.
func TestToggleWritableFolderSavesAndHotReloads(t *testing.T) {
	f := loggedIn(t, "")
	who := vault.WriteInfo{Tool: "t", App: "a"}
	if _, err := f.gateway.Vault().Create("Inbox/a.md", "x", who); err == nil {
		t.Fatal("not writable yet")
	}
	page := f.post(t, "/admin/vault/folder", url.Values{"name": {"Inbox"}, "on": {"1"}})
	if !strings.Contains(page, `class="toast"`) {
		t.Errorf("no success toast: %s", page)
	}
	if !slices.Contains(f.store.Get().Vault.WritableFolders, "Inbox") {
		t.Fatalf("config: %v", f.store.Get().Vault.WritableFolders)
	}
	onDisk, err := config.Load(f.store.Path())
	if err != nil || !slices.Contains(onDisk.Vault.WritableFolders, "Inbox") {
		t.Fatalf("file: %v %v", onDisk, err)
	}
	if _, err := f.gateway.Vault().Create("Inbox/a.md", "x", who); err != nil {
		t.Fatalf("hot reload did not apply: %v", err)
	}
	if backups, _ := f.store.Backups(); len(backups) != 1 {
		t.Errorf("no backup: %v", backups)
	}

	f.post(t, "/admin/vault/folder", url.Values{"name": {"Inbox"}})
	if _, err := f.gateway.Vault().Create("Inbox/b.md", "x", who); err == nil {
		t.Fatal("still writable after switching off")
	}
}

func TestInvalidChangeShowsErrorInlineAndSavesNothing(t *testing.T) {
	f := loggedIn(t, "")
	before := f.store.Get()
	page := f.post(t, "/admin/vault/folder", url.Values{"name": {"../outside"}, "on": {"1"}})
	if !strings.Contains(page, `class="error"`) || !strings.Contains(page, "inside the vault") {
		t.Fatalf("page: %s", page)
	}
	if f.store.Get() != before {
		t.Error("config changed")
	}
	if backups, _ := f.store.Backups(); len(backups) != 0 {
		t.Errorf("backups: %v", backups)
	}
}
