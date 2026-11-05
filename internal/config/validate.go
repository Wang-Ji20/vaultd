package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	upstreamNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	sha256HexRE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// Names under which vaultd exposes its own tools.
	reservedNamespaces = []string{"vault", "memory"}
)

// EnvPrefix marks a config value as a reference to an environment variable.
const EnvPrefix = "env:"

// Validate reports the first problem with the config. It changes nothing on
// disk, so a rejected config has no side effects.
func (c *Config) Validate() error {
	checks := []func() error{
		c.validateServer,
		c.validateVault,
		c.validateAdminAudit,
		c.validateApps,
		c.validateUpstreams,
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateServer() error {
	return ValidateListen(c.Server.Listen)
}

// ValidateListen requires a host:port on a loopback address.
func ValidateListen(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("server.listen %q: must be host:port", listen)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("server.listen %q: invalid port", listen)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("server.listen %q: must be a loopback address such as 127.0.0.1", listen)
	}
	return nil
}

func (c *Config) validateVault() error {
	v := c.Vault
	if v.Path == "" {
		return errors.New("vault.path is required")
	}
	info, err := os.Stat(v.Path)
	if err != nil {
		return fmt.Errorf("vault.path %q does not exist", v.Path)
	}
	if !info.IsDir() {
		return fmt.Errorf("vault.path %q is not a folder", v.Path)
	}
	for _, folder := range v.WritableFolders {
		if err := validateLocalPath(folder); err != nil {
			return fmt.Errorf("vault.writable_folders %q: %w", folder, err)
		}
		if v.isIgnored(folder) {
			return fmt.Errorf("vault.writable_folders %q: folder is in vault.ignore", folder)
		}
	}
	for _, folder := range v.Ignore {
		if err := validateLocalPath(folder); err != nil {
			return fmt.Errorf("vault.ignore %q: %w", folder, err)
		}
	}
	if v.MemoryFile != "" {
		if err := validateLocalPath(v.MemoryFile); err != nil {
			return fmt.Errorf("vault.memory_file %q: %w", v.MemoryFile, err)
		}
		if !strings.HasSuffix(v.MemoryFile, ".md") {
			return fmt.Errorf("vault.memory_file %q: must end in .md", v.MemoryFile)
		}
	}
	return nil
}

func (v Vault) isIgnored(folder string) bool {
	for _, segment := range strings.Split(path.Clean(folder), "/") {
		if slices.Contains(v.Ignore, segment) {
			return true
		}
	}
	return false
}

// validateLocalPath is a first check for vault-relative paths in the config.
// The vault's path jail still checks every path at the time of use.
func validateLocalPath(p string) error {
	if p == "" {
		return errors.New("must not be empty")
	}
	if strings.ContainsAny(p, "\\:\x00") || strings.HasPrefix(p, "/") {
		return errors.New("must be relative to the vault and use / as separator")
	}
	if clean := path.Clean(p); clean == ".." || strings.HasPrefix(clean, "../") {
		return errors.New("must stay inside the vault")
	}
	return nil
}

func (c *Config) validateAdminAudit() error {
	if c.Admin.SessionTTL <= 0 {
		return errors.New("admin.session_ttl must be positive")
	}
	if !slices.Contains([]string{"", "system", "light", "dark"}, c.Admin.Theme) {
		return fmt.Errorf("admin.theme %q: must be system, light or dark", c.Admin.Theme)
	}
	if c.Audit.MaxSizeMB <= 0 {
		return errors.New("audit.max_size_mb must be positive")
	}
	if c.Audit.MaxFiles <= 0 {
		return errors.New("audit.max_files must be positive")
	}
	return nil
}

func (c *Config) validateApps() error {
	seen := map[string]bool{}
	for _, app := range c.Apps {
		if app.Name == "" {
			return errors.New("apps: name is required")
		}
		if seen[app.Name] {
			return fmt.Errorf("apps: duplicate name %q", app.Name)
		}
		seen[app.Name] = true
		if !sha256HexRE.MatchString(app.TokenSHA256) {
			return fmt.Errorf("apps %q: token_sha256 must be 64 lowercase hex characters", app.Name)
		}
	}
	return nil
}

func (c *Config) validateUpstreams() error {
	seen := map[string]bool{}
	for _, up := range c.Upstreams {
		if !upstreamNameRE.MatchString(up.Name) {
			return fmt.Errorf("upstreams: name %q may only use letters, digits, _ and -", up.Name)
		}
		if slices.Contains(reservedNamespaces, up.Name) {
			return fmt.Errorf("upstreams: name %q is reserved for vaultd's own tools", up.Name)
		}
		if seen[up.Name] {
			return fmt.Errorf("upstreams: duplicate name %q", up.Name)
		}
		seen[up.Name] = true
		if err := up.validate(); err != nil {
			return fmt.Errorf("upstream %q: %w", up.Name, err)
		}
	}
	return nil
}

func (u Upstream) validate() error {
	switch u.Transport {
	case "stdio":
		if u.Command == "" {
			return errors.New("stdio transport needs a command")
		}
	case "http":
		if u.URL == "" {
			return errors.New("http transport needs a url")
		}
	default:
		return fmt.Errorf("transport %q: must be stdio or http", u.Transport)
	}
	for _, tool := range u.Allow {
		if slices.Contains(u.Locked, tool) {
			return fmt.Errorf("tool %q is both allowed and locked; locked tools can never be enabled", tool)
		}
	}
	if err := requireEnvRefs("env", u.Env); err != nil {
		return err
	}
	return requireEnvRefs("headers", u.Headers)
}

// requireEnvRefs enforces the rule that secrets never sit in the config file.
// The value cannot be told apart from a secret, so every value must be a
// reference. The message names the key only, in case the value is a secret.
func requireEnvRefs(section string, values map[string]string) error {
	for key, value := range values {
		if !strings.HasPrefix(value, EnvPrefix) || len(value) == len(EnvPrefix) {
			return fmt.Errorf("%s.%s must be an env:NAME reference, not a literal value", section, key)
		}
	}
	return nil
}
