package config

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	vault := tomlPath(t.TempDir())
	path := writeConfig(t, "[vault]\npath = \""+vault+"\"\nwritable_folders = [\"AI\"]\n"+
		"[[upstreams]]\nname = \"up\"\ntransport = \"http\"\nurl = \"http://x\"\n"+
		"headers = { Authorization = \"env:AUTH\" }\nallow = [\"a\"]\nlocked = [\"b\"]\n")
	store, err := OpenStore(path, filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatal(err)
	}
	return store, path
}

func TestUpdateSavesBacksUpAndNotifies(t *testing.T) {
	store, path := newStore(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var seen atomic.Int32
	store.OnChange(func(old, updated *Config) {
		if len(old.Vault.WritableFolders) == 1 && len(updated.Vault.WritableFolders) == 2 {
			seen.Add(1)
		}
	})
	err = store.Update(func(c *Config) error {
		c.Vault.WritableFolders = append(c.Vault.WritableFolders, "Inbox")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen.Load() != 1 {
		t.Error("listener not called")
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded, store.Get()) {
		t.Errorf("file and memory differ:\n%+v\n%+v", reloaded, store.Get())
	}
	if reloaded.Upstreams[0].Headers["Authorization"] != "env:AUTH" {
		t.Errorf("upstream lost in save: %+v", reloaded.Upstreams)
	}
	names, err := store.Backups()
	if err != nil || len(names) != 1 {
		t.Fatalf("backups %v %v", names, err)
	}
	backup, err := os.ReadFile(filepath.Join(store.backupDir, names[0]))
	if err != nil || string(backup) != string(before) {
		t.Errorf("backup must hold the old file: %q %v", backup, err)
	}
}

func TestUpdateRejectedChangesNothing(t *testing.T) {
	store, path := newStore(t)
	before, _ := os.ReadFile(path)
	old := store.Get()
	err := store.Update(func(c *Config) error {
		c.Upstreams[0].Allow = []string{"b"} // b is locked
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("got %v", err)
	}
	wantErr := errors.New("nope")
	if err := store.Update(func(*Config) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("got %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || store.Get() != old {
		t.Error("a rejected change must not touch file or memory")
	}
	if names, _ := store.Backups(); len(names) != 0 {
		t.Errorf("no backup expected: %v", names)
	}
}

func TestUpdateNeverChangesPreviousSnapshot(t *testing.T) {
	store, _ := newStore(t)
	old := store.Get()
	err := store.Update(func(c *Config) error {
		c.Vault.WritableFolders[0] = "Other"
		c.Upstreams[0].Headers["New"] = "env:X"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if old.Vault.WritableFolders[0] != "AI" || len(old.Upstreams[0].Headers) != 1 {
		t.Fatalf("snapshot changed: %+v", old)
	}
}

func TestBackupsKeepLastTenAndRestore(t *testing.T) {
	store, _ := newStore(t)
	for i := range backupsKept + 3 {
		err := store.Update(func(c *Config) error {
			c.Audit.MaxFiles = i + 1
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	names, err := store.Backups()
	if err != nil || len(names) != backupsKept {
		t.Fatalf("%d backups, %v", len(names), err)
	}
	// The newest backup holds the value from just before the last update.
	if err := store.Restore(names[0]); err != nil {
		t.Fatal(err)
	}
	if got := store.Get().Audit.MaxFiles; got != backupsKept+2 {
		t.Errorf("restored %d", got)
	}
	for _, bad := range []string{"nope.toml", "../config.toml", "config.toml"} {
		if err := store.Restore(bad); err == nil {
			t.Errorf("restore %q must fail", bad)
		}
	}
}

func TestWatchHotReload(t *testing.T) {
	store, path := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := store.Watch(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := store.Get()

	// An invalid config is ignored.
	if err := os.WriteFile(path, []byte("[server]\nlisten = \"0.0.0.0:1\"\n"+string(body)), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * reloadDebounce)
	if store.Get() != old {
		t.Fatal("invalid config must not be loaded")
	}

	// A valid one is swapped in.
	if err := os.WriteFile(path, []byte("[audit]\nmax_files = 77\n"+string(body)), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for store.Get().Audit.MaxFiles != 77 {
		if time.Now().After(deadline) {
			t.Fatal("not reloaded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReloadIgnoresIdenticalFile(t *testing.T) {
	store, _ := newStore(t)
	old := store.Get()
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	if store.Get() != old {
		t.Fatal("identical file must not count as a change")
	}
}

func TestTokens(t *testing.T) {
	token, hash := NewToken()
	other, _ := NewToken()
	if token == other || !strings.HasPrefix(token, "vd_") || HashToken(token) != hash || !sha256HexRE.MatchString(hash) {
		t.Fatalf("token %q hash %q", token, hash)
	}
	cfg := &Config{Apps: []App{{Name: "first", TokenSHA256: HashToken("x")}, {Name: "second", TokenSHA256: hash}}}
	if name, ok := cfg.FindApp(token); !ok || name != "second" {
		t.Errorf("got %q %v", name, ok)
	}
	for _, bad := range []string{"", other, hash} {
		if name, ok := cfg.FindApp(bad); ok {
			t.Errorf("%q matched %q", bad, name)
		}
	}
}
