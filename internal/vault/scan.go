package vault

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ReadRaw returns a note's bytes and file details. It refuses anything larger
// than maxSize by returning a nil slice with the details, so that callers can
// still record that the note exists.
func (v *Vault) ReadRaw(p string, maxSize int64) ([]byte, fs.FileInfo, error) {
	clean, abs, err := v.resolveNote(p)
	if err != nil {
		return nil, nil, err
	}
	info, err := statRegular(clean, abs)
	if err != nil {
		return nil, nil, err
	}
	if info.Size() > maxSize {
		return nil, info, nil
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, nil, readError(clean, err)
	}
	return raw, info, nil
}

// WalkFolders calls visit with the absolute path of every visible folder,
// starting at start, a path relative to the root.
func (v *Vault) WalkFolders(start string, visit func(abs string) error) error {
	begin := filepath.Join(v.root, filepath.FromSlash(start))
	return filepath.WalkDir(begin, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if full != v.root && !v.visible(v.relReal(full)) {
			return filepath.SkipDir
		}
		return visit(full)
	})
}

// Rel converts an absolute path inside the vault to a relative API path. It
// reports false for paths outside the root or hidden by the ignore rules.
func (v *Vault) Rel(abs string) (string, bool) {
	if !isInside(v.root, abs, foldPaths) {
		return "", false
	}
	rel := v.relReal(abs)
	if rel == "." || !v.visible(rel) {
		return "", false
	}
	return rel, true
}

// IsNotePath reports whether a path names a Markdown note.
func IsNotePath(p string) bool { return isNote(p) }

// Recent returns notes modified in the last days days, newest first.
func (v *Vault) Recent(days, limit int, now time.Time) ([]NoteInfo, error) {
	type candidate struct {
		rel  string
		info fs.FileInfo
	}
	cutoff := now.AddDate(0, 0, -days)
	var found []candidate
	err := v.WalkNotes(func(rel string, info fs.FileInfo) error {
		if !info.ModTime().Before(cutoff) {
			found = append(found, candidate{rel, info})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(found, func(i, j int) bool {
		return found[i].info.ModTime().After(found[j].info.ModTime())
	})
	out := []NoteInfo{}
	// Titles need the file's content, so read only the notes being returned.
	for _, item := range found[:min(limit, len(found))] {
		abs := filepath.Join(v.root, filepath.FromSlash(item.rel))
		out = append(out, NoteInfo{
			Path:     item.rel,
			Title:    readTitle(item.rel, abs, item.info),
			Modified: FormatTime(item.info.ModTime()),
		})
	}
	return out, nil
}
