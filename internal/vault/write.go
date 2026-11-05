package vault

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// writeMu serializes writes, so that two appends cannot read the same old
// content and lose one of the additions. It also keeps git commands from
// colliding on git's lock file. There is one vault per process, so a single
// lock is enough, and it stays valid across a config reload.
var writeMu sync.Mutex

// ErrNotWritable marks a write outside the writable folders.
var ErrNotWritable = errors.New("not writable")

// ErrExists marks a create on a path that is already taken.
var ErrExists = errors.New("already exists")

// WriteInfo identifies a write for the git commit message.
type WriteInfo struct {
	Tool string
	App  string
}

// AppendResult is the outcome of an append.
type AppendResult struct {
	Path         string `json:"path"`
	BytesWritten int    `json:"bytes_written"`
}

// Append adds a marked block to the end of a note, creating it if missing.
func (v *Vault) Append(p, text string, who WriteInfo, now time.Time) (*AppendResult, error) {
	return v.appendBlock(p, who, func(_ []byte, eol string) string {
		return eol + eol + "<!-- vaultd " + now.Format(time.RFC3339) + " " + who.App + " -->" + eol +
			normalizeEOL(text, eol) + eol
	})
}

// Remember adds one dated line to the memory file. Line breaks in text become
// spaces: each memory is exactly one line, which memory.recall relies on, and
// which stops one memory from faking others.
func (v *Vault) Remember(text string, who WriteInfo, now time.Time) (*AppendResult, error) {
	if v.memoryFile == "" {
		return nil, errors.New("vault.memory_file is not configured")
	}
	single := strings.Join(strings.Fields(text), " ")
	if single == "" {
		return nil, errors.New("text must not be empty")
	}
	line := "- " + now.Format("2006-01-02 15:04") + " (" + who.App + ") " + single
	return v.appendBlock(v.memoryFile, who, func(old []byte, eol string) string {
		if len(old) > 0 && !bytes.HasSuffix(old, []byte("\n")) {
			return eol + line + eol
		}
		return line + eol
	})
}

// appendBlock is the shared write path: block builds the text to add, given the
// note's old content and its line ending.
func (v *Vault) appendBlock(p string, who WriteInfo, block func(old []byte, eol string) string) (*AppendResult, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	clean, abs, err := v.resolveWritable(p)
	if err != nil {
		return nil, err
	}
	old, mode, err := readExisting(clean, abs)
	if err != nil {
		return nil, err
	}
	addition := block(old, detectEOL(old))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, writeError(clean)
	}
	if err := replaceAtomic(abs, append(old, addition...), mode); err != nil {
		return nil, writeError(clean)
	}
	v.autocommit(abs, clean, who)
	return &AppendResult{Path: clean, BytesWritten: len(addition)}, nil
}

// Create writes a new note. There is deliberately no way to overwrite: a
// path that is taken stays as it is.
func (v *Vault) Create(p, content string, who WriteInfo) (string, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	clean, abs, err := v.resolveWritable(p)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", writeError(clean)
	}
	if err := createExclusive(abs, []byte(content)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("%w: %q; vault.create never overwrites", ErrExists, clean)
		}
		return "", writeError(clean)
	}
	v.autocommit(abs, clean, who)
	return clean, nil
}

// writeError hides the absolute path, which the OS error would include.
func writeError(clean string) error {
	return fmt.Errorf("cannot write %q", clean)
}

// resolveWritable applies the jail, then the write policy. Both the path as
// given and its real location must be inside a writable folder, so a symlink
// cannot carry a write across the boundary in either direction.
func (v *Vault) resolveWritable(p string) (clean, abs string, err error) {
	clean, abs, err = v.resolveNote(p)
	if err != nil {
		return "", "", err
	}
	if !v.inWritableFolder(clean) || !v.inWritableFolder(v.relReal(abs)) {
		return "", "", fmt.Errorf("%w: %q is outside the writable folders %v", ErrNotWritable, clean, v.writable)
	}
	return clean, abs, nil
}

func (v *Vault) inWritableFolder(rel string) bool {
	for _, folder := range v.writable {
		clean, err := CleanRel(folder)
		if err != nil {
			continue
		}
		if clean == "." || isInside("/"+clean, "/"+rel, foldPaths) {
			return true
		}
	}
	return false
}

func readExisting(clean, abs string) ([]byte, fs.FileMode, error) {
	info, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0o644, nil
	}
	if err != nil {
		return nil, 0, writeError(clean)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%q is not a regular file", clean)
	}
	old, err := os.ReadFile(abs)
	if err != nil {
		return nil, 0, writeError(clean)
	}
	return old, info.Mode().Perm(), nil
}

// detectEOL picks the line ending most of the file uses. A new file gets LF.
func detectEOL(content []byte) string {
	crlf := bytes.Count(content, []byte("\r\n"))
	if crlf > 0 && crlf*2 >= bytes.Count(content, []byte("\n")) {
		return "\r\n"
	}
	return "\n"
}

func normalizeEOL(text, eol string) string {
	unix := strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(unix, "\n", eol)
}

// writeTemp puts data in a synced temporary file next to abs. The temp file's
// name does not end in .md, so neither the watcher nor listings care about it.
func writeTemp(abs string, data []byte, mode fs.FileMode) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".vaultd-*.tmp")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(name, mode)
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// replaceAtomic swaps the file in one step: a reader or a crash sees the whole
// old note or the whole new one. os.Rename replaces an existing file on
// Windows too.
func replaceAtomic(abs string, data []byte, mode fs.FileMode) error {
	tmp, err := writeTemp(abs, data, mode)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(abs))
	return nil
}

// createExclusive publishes a temp file under the final name with a hard link,
// which unlike rename fails if the name is taken, even if another program
// created it a moment ago. Some file systems have no hard links; there it falls
// back to creating the file directly, which is exclusive but not atomic.
func createExclusive(abs string, data []byte) error {
	tmp, err := writeTemp(abs, data, 0o644)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	err = os.Link(tmp, abs)
	if err == nil || errors.Is(err, fs.ErrExist) {
		if err == nil {
			syncDir(filepath.Dir(abs))
		}
		return err
	}
	file, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

// syncDir makes a rename survive a power cut on systems that support it. It
// is best effort: Windows cannot sync a folder, and the data is already safe.
func syncDir(dir string) {
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		handle.Close()
	}
}
