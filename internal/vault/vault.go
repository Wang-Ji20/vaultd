// Package vault gives safe access to an Obsidian vault on disk: the path
// jail, note parsing, and the read operations. Every path from a tool call
// goes through the jail before the file system sees it.
package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// MaxReadSize is the largest note vault.read returns.
const MaxReadSize = 1 << 20

// ErrNotFound marks a note or folder that does not exist.
var ErrNotFound = errors.New("not found")

// Options configures a Vault.
type Options struct {
	Root          string
	Ignore        []string
	Writable      []string
	MemoryFile    string
	GitAutocommit bool
	Logger        *slog.Logger
}

// Vault is a jailed view of one vault folder. It holds no open files and is
// safe for concurrent use.
type Vault struct {
	root          string // absolute, symlinks resolved
	ignore        []string
	writable      []string
	memoryFile    string
	gitAutocommit bool
	log           *slog.Logger
}

// New opens the vault at opts.Root.
func New(opts Options) (*Vault, error) {
	abs, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("vault path: %w", err)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("vault path: %w", err)
	}
	return &Vault{
		root:          root,
		ignore:        opts.Ignore,
		writable:      opts.Writable,
		memoryFile:    opts.MemoryFile,
		gitAutocommit: opts.GitAutocommit,
		log:           opts.Logger,
	}, nil
}

// Root returns the vault's absolute path.
func (v *Vault) Root() string { return v.root }

// MemoryFile returns the configured memory note's path, or "".
func (v *Vault) MemoryFile() string { return v.memoryFile }

// Note is a note as returned by Read.
type Note struct {
	Path        string         `json:"path"`
	Title       string         `json:"title"`
	Frontmatter map[string]any `json:"frontmatter"`
	Tags        []string       `json:"tags"`
	Content     string         `json:"content"`
	Modified    string         `json:"modified"`
	Size        int64          `json:"size"`
}

// NoteInfo is the short description of a note used in listings.
type NoteInfo struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Modified string `json:"modified"`
}

// Listing is the content of one folder.
type Listing struct {
	Folders []string   `json:"folders"`
	Notes   []NoteInfo `json:"notes"`
}

// FormatTime is the time format used in all tool results.
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Read returns one parsed note. Content excludes the frontmatter, which is
// returned separately.
func (v *Vault) Read(p string) (*Note, error) {
	clean, abs, err := v.resolveNote(p)
	if err != nil {
		return nil, err
	}
	info, err := statRegular(clean, abs)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxReadSize {
		return nil, fmt.Errorf("note %q is %d bytes; vault.read refuses files over 1 MB", clean, info.Size())
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, readError(clean, err)
	}
	parsed := Parse(clean, raw)
	return &Note{
		Path:        clean,
		Title:       parsed.Title,
		Frontmatter: parsed.Frontmatter,
		Tags:        parsed.Tags,
		Content:     parsed.Body,
		Modified:    FormatTime(info.ModTime()),
		Size:        info.Size(),
	}, nil
}

func (v *Vault) resolveNote(p string) (clean, abs string, err error) {
	clean, abs, err = v.resolve(p)
	if err != nil {
		return "", "", err
	}
	if !isNote(clean) {
		return "", "", refuse(p, "is not a note: only .md files are notes")
	}
	return clean, abs, nil
}

// statRegular also rejects pipes and devices, which would block or misbehave
// if opened. Errors never show the absolute path to the caller.
func statRegular(clean, abs string) (fs.FileInfo, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return nil, readError(clean, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", clean)
	}
	return info, nil
}

func readError(clean string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %q", ErrNotFound, clean)
	}
	return fmt.Errorf("cannot read %q", clean)
}

// List returns the folders and notes directly inside folder, sorted by name.
func (v *Vault) List(folder string) (*Listing, error) {
	clean, abs, err := v.resolve(folder)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, readError(clean, err)
	}
	out := &Listing{Folders: []string{}, Notes: []NoteInfo{}}
	for _, entry := range entries {
		rel := path.Join(clean, entry.Name())
		if !v.visible(rel) {
			continue
		}
		switch {
		case entry.IsDir():
			out.Folders = append(out.Folders, rel)
		case entry.Type().IsRegular() && isNote(rel):
			if info, err := v.Describe(rel); err == nil {
				out.Notes = append(out.Notes, *info)
			}
		}
	}
	return out, nil
}

// visible says whether a walked entry may be shown: its name passes the jail's
// text checks and it is not ignored.
func (v *Vault) visible(rel string) bool {
	clean, err := CleanRel(rel)
	return err == nil && !v.ignored(clean)
}

// Describe returns the title and time of one note.
func (v *Vault) Describe(p string) (*NoteInfo, error) {
	clean, abs, err := v.resolveNote(p)
	if err != nil {
		return nil, err
	}
	info, err := statRegular(clean, abs)
	if err != nil {
		return nil, err
	}
	return &NoteInfo{Path: clean, Title: readTitle(clean, abs, info), Modified: FormatTime(info.ModTime())}, nil
}

// readTitle does not load oversized files: for those the name will do.
func readTitle(clean, abs string, info fs.FileInfo) string {
	if info.Size() <= MaxReadSize {
		if raw, err := os.ReadFile(abs); err == nil {
			return Parse(clean, raw).Title
		}
	}
	return strings.TrimSuffix(path.Base(clean), path.Ext(clean))
}

// WalkNotes calls visit for every visible note under the root in lexical
// order. It does not follow symlinks, so a link loop cannot trap it. A folder
// that cannot be read is skipped rather than ending the walk.
func (v *Vault) WalkNotes(visit func(rel string, info fs.FileInfo) error) error {
	return filepath.WalkDir(v.root, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if full == v.root {
			return nil
		}
		rel := v.relReal(full)
		if !v.visible(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !isNote(rel) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		return visit(rel, info)
	})
}

// Hit is one search result.
type Hit struct {
	Path     string   `json:"path"`
	Title    string   `json:"title"`
	Tags     []string `json:"tags"`
	Snippet  string   `json:"snippet"`
	Score    float64  `json:"score"`
	Modified string   `json:"modified"`
}
