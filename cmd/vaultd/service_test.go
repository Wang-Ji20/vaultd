package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestServiceConfig(t *testing.T) {
	cfg := serviceConfig(&paths{config: "/c/config.toml", data: "/d"})
	if cfg.Name != "vaultd" || cfg.DisplayName != "Vault Gateway (vaultd)" {
		t.Errorf("names: %+v", cfg)
	}
	if want := []string{"run", "--config", "/c/config.toml", "--data-dir", "/d"}; !slices.Equal(cfg.Arguments, want) {
		t.Errorf("arguments: %v", cfg.Arguments)
	}
	if cfg.Option["UserService"] != true {
		t.Error("must be a user service on Linux")
	}
}

func TestInstallRefusesBadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[server]\nlisten = \"0.0.0.0:1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := dispatch([]string{"install", "--config", path}, &stdout, &stderr); err == nil {
		t.Fatal("a service that cannot start must not be installed")
	}
}

func TestPathsResolveMakesAbsolute(t *testing.T) {
	where := &paths{config: "config.toml", data: "data"}
	if err := where.resolve(); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(where.config) || !filepath.IsAbs(where.data) {
		t.Fatalf("got %+v", where)
	}
}

func runInit(t *testing.T, configPath, input string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := initCommand([]string{"--config", configPath}, strings.NewReader(input), &stdout, &stderr)
	return strings.TrimSpace(stdout.String()), err
}

func TestInitWithDefaults(t *testing.T) {
	vaultDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "new", "config.toml")
	token, err := runInit(t, configPath, vaultDir+"\n\n\n")
	if err != nil {
		t.Fatal(err)
	}
	where := &paths{config: configPath, data: t.TempDir()}
	store, err := where.openStore()
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.Get()
	if !slices.Equal(cfg.Vault.WritableFolders, []string{"AI", "Inbox"}) || cfg.Vault.MemoryFile != "AI/memory.md" || !cfg.Vault.GitAutocommit {
		t.Errorf("got %+v", cfg.Vault)
	}
	if name, ok := cfg.FindApp(token); !ok || name != "claude-desktop" {
		t.Errorf("token for %q, %v", name, ok)
	}
	for _, folder := range []string{"AI", "Inbox"} {
		if _, err := os.Stat(filepath.Join(vaultDir, folder)); err != nil {
			t.Error(err)
		}
	}
	saved, err := os.ReadFile(configPath)
	if err != nil || strings.Contains(string(saved), token) {
		t.Errorf("plain token stored: %v", err)
	}
}

func TestInitWithAnswers(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if _, err := runInit(t, configPath, t.TempDir()+"\nNotes/AI , Drafts\ncursor\n"); err != nil {
		t.Fatal(err)
	}
	where := &paths{config: configPath, data: t.TempDir()}
	store, err := where.openStore()
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.Get()
	if !slices.Equal(cfg.Vault.WritableFolders, []string{"Notes/AI", "Drafts"}) || cfg.Vault.MemoryFile != "Notes/AI/memory.md" || cfg.Apps[0].Name != "cursor" {
		t.Errorf("got %+v", cfg)
	}
}

func TestInitRefusals(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if _, err := runInit(t, configPath, "\n"); err == nil {
		t.Error("no vault folder")
	}
	if _, err := runInit(t, configPath, filepath.Join(t.TempDir(), "missing")+"\n"); err == nil {
		t.Error("missing vault folder")
	}
	if _, err := os.Stat(configPath); err == nil {
		t.Error("a failed init must write nothing")
	}
	if _, err := runInit(t, configPath, t.TempDir()+"\n\n\n"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(configPath)
	if _, err := runInit(t, configPath, t.TempDir()+"\n\n\n"); err == nil {
		t.Error("must not replace a config")
	}
	after, _ := os.ReadFile(configPath)
	if !bytes.Equal(before, after) {
		t.Error("config changed")
	}
}
