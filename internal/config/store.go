package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
)

const (
	backupsKept    = 10
	reloadDebounce = 200 * time.Millisecond
	backupPrefix   = "config."
	backupSuffix   = ".toml"
)

// Store holds the current config and owns the file on disk. Readers get an
// immutable snapshot, so a reload can never change a config mid-request.
type Store struct {
	path      string
	backupDir string
	current   atomic.Pointer[Config]

	saveMu    sync.Mutex
	listeners []func(old, updated *Config)
}

// OpenStore loads the config at path. Backups go to backupDir.
func OpenStore(path, backupDir string) (*Store, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, backupDir: backupDir}
	s.current.Store(cfg)
	return s, nil
}

// Get returns the current config. Callers must not change it.
func (s *Store) Get() *Config { return s.current.Load() }

// Path returns the config file's location.
func (s *Store) Path() string { return s.path }

// OnChange registers fn to run after each change. Register before calling
// Watch.
func (s *Store) OnChange(fn func(old, updated *Config)) {
	s.listeners = append(s.listeners, fn)
}

// Update applies change to a copy of the config, then validates, backs up the
// old file, saves and swaps. If any step fails, nothing has changed.
func (s *Store) Update(change func(*Config) error) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	updated, err := clone(s.Get())
	if err != nil {
		return err
	}
	if err := change(updated); err != nil {
		return err
	}
	return s.commit(updated)
}

func (s *Store) commit(updated *Config) error {
	if err := updated.Validate(); err != nil {
		return err
	}
	if err := updated.EnsureWritableFolders(); err != nil {
		return err
	}
	if err := s.backup(); err != nil {
		return err
	}
	if err := writeAtomic(s.path, updated); err != nil {
		return err
	}
	s.swap(updated)
	return nil
}

func (s *Store) swap(updated *Config) {
	old := s.current.Swap(updated)
	for _, listener := range s.listeners {
		listener(old, updated)
	}
}

func clone(c *Config) (*Config, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return nil, fmt.Errorf("copy config: %w", err)
	}
	out := Default()
	if _, err := toml.Decode(buf.String(), out); err != nil {
		return nil, fmt.Errorf("copy config: %w", err)
	}
	return out, nil
}

// writeAtomic replaces the file in one step, so a crash or a reader never sees
// half a config. The mode stays private: the file lists token hashes.
func writeAtomic(path string, c *Config) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return replaceFile(path, buf.Bytes())
}

func replaceFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("save config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("save config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// backup copies the current file into the backup folder and trims old copies.
func (s *Store) backup() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("back up config: %w", err)
	}
	if err := os.MkdirAll(s.backupDir, 0o755); err != nil {
		return fmt.Errorf("back up config: %w", err)
	}
	name := backupPrefix + time.Now().UTC().Format("20060102T150405.000000") + backupSuffix
	if err := os.WriteFile(filepath.Join(s.backupDir, name), data, 0o600); err != nil {
		return fmt.Errorf("back up config: %w", err)
	}
	return s.trimBackups()
}

func (s *Store) trimBackups() error {
	names, err := s.Backups()
	if err != nil {
		return err
	}
	for _, name := range names[min(backupsKept, len(names)):] {
		if err := os.Remove(filepath.Join(s.backupDir, name)); err != nil {
			return fmt.Errorf("trim backups: %w", err)
		}
	}
	return nil
}

// Backups lists backup file names, newest first.
func (s *Store) Backups() ([]string, error) {
	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list backups: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if isBackupName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

func isBackupName(name string) bool {
	return strings.HasPrefix(name, backupPrefix) && strings.HasSuffix(name, backupSuffix) &&
		filepath.Base(name) == name
}

// Restore makes a backup the current config. The config being replaced is
// itself backed up, so a restore can be undone.
func (s *Store) Restore(name string) error {
	names, err := s.Backups()
	if err != nil {
		return err
	}
	if !slices.Contains(names, name) {
		return fmt.Errorf("no backup named %q", name)
	}
	cfg, err := Load(filepath.Join(s.backupDir, name))
	if err != nil {
		return err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return s.commit(cfg)
}

// Watch reloads the config when the file changes, until ctx ends. An invalid
// file is logged and ignored: the gateway keeps running on the last good config.
func (s *Store) Watch(ctx context.Context, logger *slog.Logger) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("watch config: %w", err)
	}
	// Watch the folder, not the file: saving by rename replaces the file, and a
	// watch on the old file would never fire again.
	if err := watcher.Add(filepath.Dir(s.path)); err != nil {
		watcher.Close()
		return fmt.Errorf("watch config: %w", err)
	}
	go func() {
		defer watcher.Close()
		s.watchLoop(ctx, watcher, logger)
	}()
	return nil
}

func (s *Store) watchLoop(ctx context.Context, watcher *fsnotify.Watcher, logger *slog.Logger) {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(event.Name) == filepath.Clean(s.path) {
				timer.Reset(reloadDebounce)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logger.Warn("config watcher error", "error", err)
		case <-timer.C:
			if err := s.Reload(); err != nil {
				logger.Error("config change rejected; keeping the previous config", "error", err)
			}
		}
	}
}

// Reload reads the file again and swaps it in if it is valid and different.
func (s *Store) Reload() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	cfg, err := Load(s.path)
	if err != nil {
		return err
	}
	if same, err := equal(cfg, s.Get()); err != nil || same {
		return err
	}
	s.swap(cfg)
	return nil
}

func equal(a, b *Config) (bool, error) {
	var bufA, bufB bytes.Buffer
	if err := toml.NewEncoder(&bufA).Encode(a); err != nil {
		return false, err
	}
	if err := toml.NewEncoder(&bufB).Encode(b); err != nil {
		return false, err
	}
	return bytes.Equal(bufA.Bytes(), bufB.Bytes()), nil
}
