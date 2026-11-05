package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
)

// These table tests run unchanged on Windows and Linux: CleanRel treats both
// path styles the same on every platform.
func TestCleanRelRejects(t *testing.T) {
	cases := map[string]string{
		"unix absolute":        "/etc/passwd",
		"double slash":         "//server/share/a.md",
		"dot dot":              "..",
		"dot dot note":         "../a.md",
		"nested escape":        "a/../../b.md",
		"deep escape":          "a/b/../../../c.md",
		"NUL":                  "a\x00.md",
		"NUL truncation":       "a.md\x00.txt",
		"drive letter":         "C:",
		"drive path":           "C:/Windows/win.ini",
		"drive backslash":      `C:\Windows\win.ini`,
		"drive relative":       "C:a.md",
		"lowercase drive":      "c:/a.md",
		"UNC":                  `\\server\share\a.md`,
		"UNC extended":         `\\?\C:\a.md`,
		"device namespace":     `\\.\COM1`,
		"backslash root":       `\Windows`,
		"backslash escape":     `a\..\..\b.md`,
		"alternate stream":     "a.md:secret",
		"alternate data type":  "a.md::$DATA",
		"stream in folder":     "a:b/c.md",
		"trailing dot":         "secret./a.md",
		"trailing dot note":    "a.md.",
		"trailing space":       "secret /a.md",
		"device name":          "NUL",
		"device with ext":      "con.md",
		"device mixed case":    "a/CoM1.md",
		"line printer":         "LPT9.md",
		"dotted escape folder": "a/.. /b.md",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := CleanRel(input); !errors.Is(err, ErrPathNotAllowed) {
				t.Fatalf("CleanRel(%q) = %q, %v; want ErrPathNotAllowed", input, got, err)
			}
		})
	}
}

func TestCleanRelAccepts(t *testing.T) {
	cases := map[string]string{
		"":                 ".",
		".":                ".",
		"./":               ".",
		"a.md":             "a.md",
		"./a.md":           "a.md",
		"a//b.md":          "a/b.md",
		"a/./b.md":         "a/b.md",
		"a/../b.md":        "b.md",
		"folder/":          "folder",
		"Notes/Größe é.md": "Notes/Größe é.md",
		"...md":            "...md",
		"console.md":       "console.md",
		"com10.md":         "com10.md",
	}
	for input, want := range cases {
		if got, err := CleanRel(input); err != nil || got != want {
			t.Errorf("CleanRel(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

// Whatever comes in, an accepted path never leads upward or carries a
// character that could change its meaning.
func TestCleanRelProperty(t *testing.T) {
	property := func(input string) bool {
		clean, err := CleanRel(input)
		if err != nil {
			return true
		}
		for _, segment := range strings.Split(clean, "/") {
			if segment == ".." {
				return false
			}
		}
		return !strings.ContainsAny(clean, "\\:\x00") && !strings.HasPrefix(clean, "/")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 5000}); err != nil {
		t.Fatal(err)
	}
}

func TestIsInside(t *testing.T) {
	cases := []struct {
		name, root, target string
		fold, want         bool
	}{
		{"unix child", "/v", "/v/a.md", false, true},
		{"unix self", "/v", "/v", false, true},
		{"unix sibling prefix", "/v", "/vault2/a.md", false, false},
		{"unix parent", "/v/w", "/v", false, false},
		{"unix case matters", "/v", "/V/a.md", false, false},
		{"unix root", "/", "/a.md", false, true},
		{"windows child", `C:\V`, `C:\V\a.md`, true, true},
		{"windows case folded", `C:\Vault`, `c:\vault\A.md`, true, true},
		{"windows sibling prefix", `C:\V`, `C:\V2\a.md`, true, false},
		{"windows other drive", `C:\V`, `D:\V\a.md`, true, false},
		{"windows trailing sep", `C:\V\`, `C:\V\a.md`, true, true},
		{"windows UNC outside", `C:\V`, `\\server\V\a.md`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isInside(tc.root, tc.target, tc.fold); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func newJail(t *testing.T) (*Vault, string) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "a.md", "hello")
	write(t, root, ".git/config.md", "secret")
	write(t, root, "sub/.obsidian/x.md", "secret")
	write(t, root, "private/b.md", "secret")
	v, err := New(Options{Root: root, Ignore: []string{".git", ".obsidian", "private/hidden"}})
	if err != nil {
		t.Fatal(err)
	}
	return v, root
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
}

func TestResolveIgnoredFolders(t *testing.T) {
	v, _ := newJail(t)
	for _, p := range []string{".git/config.md", ".git", "sub/.obsidian/x.md", "private/hidden/a.md"} {
		if _, _, err := v.resolve(p); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("%s: got %v", p, err)
		}
	}
	if _, _, err := v.resolve("private/b.md"); err != nil {
		t.Errorf("a path entry must only match that path: %v", err)
	}
}

func TestResolveSymlinks(t *testing.T) {
	v, root := newJail(t)
	outside := t.TempDir()
	write(t, outside, "out.md", "secret")

	symlink(t, filepath.Join(outside, "out.md"), filepath.Join(root, "file-escape.md"))
	symlink(t, outside, filepath.Join(root, "dir-escape"))
	symlink(t, filepath.Join(outside, "new.md"), filepath.Join(root, "dangling.md"))
	symlink(t, filepath.Join(root, ".git"), filepath.Join(root, "to-ignored"))
	symlink(t, filepath.Join(root, "a.md"), filepath.Join(root, "inside.md"))

	for _, p := range []string{
		"file-escape.md",
		"dir-escape/out.md",
		"dir-escape/does-not-exist.md",
		"dir-escape/new/deep.md",
		"dangling.md",
		"to-ignored/config.md",
	} {
		if _, _, err := v.resolve(p); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("%s: got %v, want refusal", p, err)
		}
	}
	if _, abs, err := v.resolve("inside.md"); err != nil || filepath.Base(abs) != "a.md" {
		t.Errorf("link inside the vault: %s, %v", abs, err)
	}
}

func TestResolveThroughSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	write(t, real, "a.md", "x")
	link := filepath.Join(t.TempDir(), "vault")
	symlink(t, real, link)
	v, err := New(Options{Root: link})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.resolve("a.md"); err != nil {
		t.Fatal(err)
	}
}

func TestResolveMissingFileInsideVault(t *testing.T) {
	v, root := newJail(t)
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	_, abs, err := v.resolve("new/deep/note.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(real, "new", "deep", "note.md"); abs != want {
		t.Fatalf("got %s, want %s", abs, want)
	}
}

func TestResolveThroughFileFails(t *testing.T) {
	v, _ := newJail(t)
	if _, _, err := v.resolve("a.md/child.md"); !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("got %v", err)
	}
}
