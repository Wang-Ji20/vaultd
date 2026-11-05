package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validConfig(t *testing.T) *Config {
	t.Helper()
	cfg := Default()
	cfg.Vault.Path = t.TempDir()
	cfg.Vault.WritableFolders = []string{"AI"}
	cfg.Apps = []App{{Name: "a", TokenSHA256: hash}}
	cfg.Upstreams = []Upstream{{Name: "up", Transport: "stdio", Command: "x", Allow: []string{"read"}, Locked: []string{"write"}}}
	return cfg
}

func TestValidateAcceptsGoodConfig(t *testing.T) {
	if err := validConfig(t).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name, want string
		mutate     func(*Config)
	}{
		{"all interfaces", "loopback", func(c *Config) { c.Server.Listen = "0.0.0.0:8765" }},
		{"empty host", "loopback", func(c *Config) { c.Server.Listen = ":8765" }},
		{"public host", "loopback", func(c *Config) { c.Server.Listen = "example.com:80" }},
		{"lan address", "loopback", func(c *Config) { c.Server.Listen = "192.168.1.5:80" }},
		{"no port", "host:port", func(c *Config) { c.Server.Listen = "127.0.0.1" }},
		{"bad port", "port", func(c *Config) { c.Server.Listen = "127.0.0.1:99999" }},
		{"no vault", "required", func(c *Config) { c.Vault.Path = "" }},
		{"missing vault", "does not exist", func(c *Config) { c.Vault.Path = "/does/not/exist" }},
		{"writable escape", "inside the vault", func(c *Config) { c.Vault.WritableFolders = []string{"../x"} }},
		{"writable absolute", "relative", func(c *Config) { c.Vault.WritableFolders = []string{"/etc"} }},
		{"writable ignored", "vault.ignore", func(c *Config) { c.Vault.WritableFolders = []string{".git/x"} }},
		{"memory not note", ".md", func(c *Config) { c.Vault.MemoryFile = "AI/memory.txt" }},
		{"ttl", "session_ttl", func(c *Config) { c.Admin.SessionTTL = 0 }},
		{"theme", "theme", func(c *Config) { c.Admin.Theme = "pink" }},
		{"audit size", "max_size_mb", func(c *Config) { c.Audit.MaxSizeMB = 0 }},
		{"audit files", "max_files", func(c *Config) { c.Audit.MaxFiles = 0 }},
		{"duplicate app", "duplicate", func(c *Config) { c.Apps = append(c.Apps, c.Apps[0]) }},
		{"bad token hash", "token_sha256", func(c *Config) { c.Apps[0].TokenSHA256 = "plaintext" }},
		{"duplicate upstream", "duplicate", func(c *Config) { c.Upstreams = append(c.Upstreams, c.Upstreams[0]) }},
		{"reserved namespace", "reserved", func(c *Config) { c.Upstreams[0].Name = "vault" }},
		{"dotted upstream", "letters", func(c *Config) { c.Upstreams[0].Name = "a.b" }},
		{"bad transport", "transport", func(c *Config) { c.Upstreams[0].Transport = "pigeon" }},
		{"no command", "command", func(c *Config) { c.Upstreams[0].Command = "" }},
		{"no url", "url", func(c *Config) { c.Upstreams[0].Transport = "http" }},
		{"allowed and locked", "locked", func(c *Config) { c.Upstreams[0].Allow = []string{"write"} }},
		{"literal env", "env:NAME", func(c *Config) { c.Upstreams[0].Env = map[string]string{"T": "hunter2"} }},
		{"literal header", "env:NAME", func(c *Config) { c.Upstreams[0].Headers = map[string]string{"A": "hunter2"} }},
		{"empty env ref", "env:NAME", func(c *Config) { c.Upstreams[0].Env = map[string]string{"T": "env:"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig(t)
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateErrorNeverShowsSecretValue(t *testing.T) {
	cfg := validConfig(t)
	cfg.Upstreams[0].Env = map[string]string{"T": "hunter2"}
	if err := cfg.Validate(); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error must exist and hide the value: %v", err)
	}
}

func TestValidateListenAcceptsLoopback(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:8765", "127.0.0.1:0", "[::1]:8765", "localhost:8765", "127.1.2.3:80"} {
		if err := ValidateListen(listen); err != nil {
			t.Errorf("%s: %v", listen, err)
		}
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func tomlPath(p string) string { return strings.ReplaceAll(p, `\`, `/`) }

func TestLoadAppliesDefaultsAndCreatesFolders(t *testing.T) {
	vault := t.TempDir()
	path := writeConfig(t, "[vault]\npath = \""+tomlPath(vault)+"\"\nwritable_folders = [\"AI/deep\"]\n[admin]\nsession_ttl = \"2h\"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:8765" || cfg.Admin.SessionTTL.Hours() != 2 {
		t.Fatalf("unexpected values: %+v", cfg)
	}
	if info, err := os.Stat(filepath.Join(vault, "AI", "deep")); err != nil || !info.IsDir() {
		t.Fatalf("writable folder not created: %v", err)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	path := writeConfig(t, "[vault]\npath = \""+tomlPath(t.TempDir())+"\"\nwritable_foldrs = []\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "writable_foldrs") {
		t.Fatalf("got %v", err)
	}
}

func TestEnsureWritableFoldersRejectsSymlinkEscape(t *testing.T) {
	vault, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(vault, "AI")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	cfg := Default()
	cfg.Vault.Path = vault
	cfg.Vault.WritableFolders = []string{"AI"}
	if err := cfg.EnsureWritableFolders(); err == nil {
		t.Fatal("want error for a folder leading outside the vault")
	}
}

func TestDefaultLocations(t *testing.T) {
	env := map[string]string{"APPDATA": `C:\A`, "LOCALAPPDATA": `C:\L`}
	getenv := func(k string) string { return env[k] }
	home := func() (string, error) { return "/home/me", nil }

	if dir, _ := configDir("linux", getenv, home); dir != filepath.Join("/home/me", ".config", "vaultd") {
		t.Errorf("linux fallback: %s", dir)
	}
	env["XDG_CONFIG_HOME"] = "/xdg"
	if dir, _ := configDir("linux", getenv, home); dir != filepath.Join("/xdg", "vaultd") {
		t.Errorf("xdg: %s", dir)
	}
	if dir, _ := dataDir("linux", getenv, home); dir != filepath.Join("/home/me", ".local", "share", "vaultd") {
		t.Errorf("linux data: %s", dir)
	}
	if dir, _ := configDir("windows", getenv, home); dir != filepath.Join(`C:\A`, "vaultd") {
		t.Errorf("windows config: %s", dir)
	}
	if dir, _ := dataDir("windows", getenv, home); dir != filepath.Join(`C:\L`, "vaultd") {
		t.Errorf("windows data: %s", dir)
	}
	delete(env, "APPDATA")
	if _, err := configDir("windows", getenv, func() (string, error) { return "", errors.New("no") }); err == nil {
		t.Error("want error when APPDATA is unset")
	}
}
