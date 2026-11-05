// Package config loads, validates and saves vaultd's config.toml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the parsed form of config.toml.
type Config struct {
	Server    Server     `toml:"server"`
	Vault     Vault      `toml:"vault"`
	Admin     Admin      `toml:"admin"`
	Audit     Audit      `toml:"audit"`
	Apps      []App      `toml:"apps"`
	Upstreams []Upstream `toml:"upstreams"`
}

// Server holds the network settings.
type Server struct {
	Listen string `toml:"listen"`
}

// Vault describes the Obsidian vault and the write policy for it.
type Vault struct {
	Path            string   `toml:"path"`
	WritableFolders []string `toml:"writable_folders"`
	Ignore          []string `toml:"ignore"`
	MemoryFile      string   `toml:"memory_file"`
	GitAutocommit   bool     `toml:"git_autocommit"`
}

// Admin holds the admin portal settings.
type Admin struct {
	PasswordHash string        `toml:"password_hash"`
	SessionTTL   time.Duration `toml:"session_ttl"`
	Theme        string        `toml:"theme"`
}

// Audit holds the audit log settings.
type Audit struct {
	MaxSizeMB    int  `toml:"max_size_mb"`
	MaxFiles     int  `toml:"max_files"`
	LogArguments bool `toml:"log_arguments"`
}

// App is a client application allowed to use the endpoint.
type App struct {
	Name        string `toml:"name"`
	TokenSHA256 string `toml:"token_sha256"`
}

// Upstream is another MCP server reached through the gateway.
type Upstream struct {
	Name      string            `toml:"name"`
	Transport string            `toml:"transport"`
	Command   string            `toml:"command,omitempty"`
	Args      []string          `toml:"args,omitempty"`
	Env       map[string]string `toml:"env,omitempty"`
	URL       string            `toml:"url,omitempty"`
	Headers   map[string]string `toml:"headers,omitempty"`
	Allow     []string          `toml:"allow"`
	Locked    []string          `toml:"locked"`
}

// Default returns a config with every optional setting at its default.
func Default() *Config {
	return &Config{
		Server: Server{Listen: "127.0.0.1:8765"},
		Vault: Vault{
			Ignore:     []string{".obsidian", ".trash", ".git"},
			MemoryFile: "AI/memory.md",
		},
		Admin: Admin{SessionTTL: 12 * time.Hour, Theme: "system"},
		Audit: Audit{MaxSizeMB: 10, MaxFiles: 5, LogArguments: true},
	}
}

// Load reads, validates and prepares the config at path. Writable folders
// that do not exist yet are created.
func Load(path string) (*Config, error) {
	cfg := Default()
	meta, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	// A misspelled key such as "lockd" must not silently drop a safety rule.
	if unknown := meta.Undecoded(); len(unknown) > 0 {
		return nil, fmt.Errorf("config: unknown setting %q", unknown[0].String())
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := cfg.EnsureWritableFolders(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// EnsureWritableFolders creates missing writable folders and rejects any that
// a symlink leads outside the vault.
func (c *Config) EnsureWritableFolders() error {
	root, err := filepath.EvalSymlinks(c.Vault.Path)
	if err != nil {
		return fmt.Errorf("vault.path: %w", err)
	}
	for _, folder := range c.Vault.WritableFolders {
		full := filepath.Join(root, filepath.FromSlash(folder))
		if err := os.MkdirAll(full, 0o755); err != nil {
			return fmt.Errorf("writable folder %q: %w", folder, err)
		}
		real, err := filepath.EvalSymlinks(full)
		if err != nil {
			return fmt.Errorf("writable folder %q: %w", folder, err)
		}
		rel, err := filepath.Rel(root, real)
		if err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("writable folder %q leads outside the vault", folder)
		}
	}
	return nil
}

// DefaultPath returns the platform's default config file location.
func DefaultPath() (string, error) {
	dir, err := configDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// DefaultDataDir returns the platform's default directory for the index,
// the audit log and config backups.
func DefaultDataDir() (string, error) {
	return dataDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
}

func configDir(goos string, getenv func(string) string, home func() (string, error)) (string, error) {
	if goos == "windows" {
		return envDir(getenv, "APPDATA")
	}
	if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "vaultd"), nil
	}
	h, err := home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "vaultd"), nil
}

func dataDir(goos string, getenv func(string) string, home func() (string, error)) (string, error) {
	if goos == "windows" {
		return envDir(getenv, "LOCALAPPDATA")
	}
	h, err := home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".local", "share", "vaultd"), nil
}

func envDir(getenv func(string) string, name string) (string, error) {
	base := getenv(name)
	if base == "" {
		return "", errors.New(name + " is not set")
	}
	return filepath.Join(base, "vaultd"), nil
}
