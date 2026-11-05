package vault

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// foldPaths says whether this platform's file names ignore case.
var foldPaths = runtime.GOOS == "windows"

// ErrPathNotAllowed marks a path the jail refuses.
var ErrPathNotAllowed = errors.New("path not allowed")

// deviceNameRE matches names Windows reserves for devices, with or without an
// extension: "NUL" and "nul.md" both open the null device there.
var deviceNameRE = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$`)

// CleanRel validates an API path and returns its clean form, or "." for the
// vault root. It looks only at the text of the path.
//
// The rules are the same on every platform. A path that is dangerous on
// Windows is refused on Linux too, so a vault behaves the same on both. This
// costs nothing in practice: Obsidian itself forbids ":" and "\" in names.
func CleanRel(p string) (string, error) {
	switch {
	case strings.ContainsRune(p, 0):
		return "", refuse(p, "contains a NUL byte")
	case strings.Contains(p, `\`):
		// Covers UNC paths (\\server\share) and Windows separators.
		return "", refuse(p, `must use / as separator`)
	case strings.Contains(p, ":"):
		// Covers drive letters (C:) and alternate data streams (a.md:x).
		return "", refuse(p, `must not contain ":"`)
	case strings.HasPrefix(p, "/"):
		return "", refuse(p, "must be relative to the vault root")
	}
	clean := path.Clean(p)
	if p == "" || clean == "." {
		return ".", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", refuse(p, "leads outside the vault")
	}
	for _, segment := range strings.Split(clean, "/") {
		if err := checkSegment(p, segment); err != nil {
			return "", err
		}
	}
	return clean, nil
}

// checkSegment refuses names Windows would silently treat as something else.
// "secret. " opens "secret" there, which could slip past a later name check.
func checkSegment(p, segment string) error {
	if strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
		return refuse(p, "names must not end in a dot or space")
	}
	if deviceNameRE.MatchString(segment) {
		return refuse(p, "uses a reserved device name")
	}
	return nil
}

func refuse(p, why string) error {
	return fmt.Errorf("%w: %q %s", ErrPathNotAllowed, p, why)
}

// isInside reports whether target equals root or lies below it. Both are
// absolute paths, in either Windows or Unix style. With fold set, case is
// ignored, as on Windows. It works on the text alone so that both styles can
// be tested on any platform.
func isInside(root, target string, fold bool) bool {
	root, target = normalize(root, fold), normalize(target, fold)
	if root == target {
		return true
	}
	if !strings.HasSuffix(root, "/") {
		root += "/"
	}
	return strings.HasPrefix(target, root)
}

func normalize(p string, fold bool) string {
	if fold {
		p = strings.ToLower(strings.ReplaceAll(p, `\`, `/`))
	}
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// evalExisting resolves symlinks in the part of p that exists and keeps the
// missing rest as written. A link whose target is missing counts as existing
// and fails to resolve: writing through it could create a file anywhere.
func evalExisting(p string) (string, error) {
	var missing []string
	current := p
	for {
		_, err := os.Lstat(current)
		if err == nil {
			real, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				real = filepath.Join(real, missing[i])
			}
			return real, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// resolve runs the whole jail: text checks, ignore rules, symlink resolution,
// containment, then ignore rules again on the real location. It returns the
// clean API path and the real absolute path.
func (v *Vault) resolve(p string) (clean, abs string, err error) {
	clean, err = CleanRel(p)
	if err != nil {
		return "", "", err
	}
	if v.ignored(clean) {
		return "", "", refuse(p, "is in an ignored folder")
	}
	abs, err = evalExisting(filepath.Join(v.root, filepath.FromSlash(clean)))
	if err != nil {
		return "", "", refuse(p, "cannot be resolved")
	}
	if !isInside(v.root, abs, foldPaths) {
		return "", "", refuse(p, "leads outside the vault")
	}
	if v.ignored(v.relReal(abs)) {
		return "", "", refuse(p, "leads into an ignored folder")
	}
	return clean, abs, nil
}

// relReal returns abs, a path already known to be inside the root, relative
// to the root with / separators.
func (v *Vault) relReal(abs string) string {
	rel, err := filepath.Rel(v.root, abs)
	if err != nil {
		return "."
	}
	return filepath.ToSlash(rel)
}

// ignored reports whether a clean relative path falls under an ignore entry.
// A plain name such as ".git" matches at any depth. An entry with a slash
// matches only that path from the root.
func (v *Vault) ignored(rel string) bool {
	if rel == "." {
		return false
	}
	segments := strings.Split(rel, "/")
	for _, entry := range v.ignore {
		if strings.Contains(entry, "/") {
			if isInside("/"+entry, "/"+rel, foldPaths) {
				return true
			}
			continue
		}
		for _, segment := range segments {
			if sameName(segment, entry) {
				return true
			}
		}
	}
	return false
}

func sameName(a, b string) bool {
	if foldPaths {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// isNote reports whether a path names a Markdown note.
func isNote(p string) bool {
	return strings.EqualFold(path.Ext(p), ".md")
}
