package vault

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	testWho = WriteInfo{Tool: "vault.append", App: "tester"}
	testNow = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
)

func writableVault(t *testing.T, git bool) *Vault {
	t.Helper()
	root := t.TempDir()
	write(t, root, "Private/secret.md", "secret")
	write(t, root, "AI/existing.md", "start")
	v, err := New(Options{
		Root:          root,
		Ignore:        []string{".git", ".obsidian"},
		Writable:      []string{"AI", "Inbox/sub"},
		MemoryFile:    "AI/memory.md",
		GitAutocommit: git,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func content(t *testing.T, v *Vault, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(v.root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAppendFormat(t *testing.T) {
	v := writableVault(t, false)
	res, err := v.Append("AI/existing.md", "hello", testWho, testNow)
	if err != nil {
		t.Fatal(err)
	}
	addition := "\n\n<!-- vaultd 2026-03-04T05:06:07Z tester -->\nhello\n"
	if got := content(t, v, "AI/existing.md"); got != "start"+addition {
		t.Fatalf("got %q", got)
	}
	if res.Path != "AI/existing.md" || res.BytesWritten != len(addition) {
		t.Fatalf("got %+v", res)
	}
}

func TestAppendCreatesMissingNoteAndFolders(t *testing.T) {
	v := writableVault(t, false)
	if _, err := v.Append("AI/deep/er/new.md", "hello", testWho, testNow); err != nil {
		t.Fatal(err)
	}
	if got := content(t, v, "AI/deep/er/new.md"); !strings.HasSuffix(got, "hello\n") {
		t.Fatalf("got %q", got)
	}
}

func TestAppendPreservesLineEndings(t *testing.T) {
	cases := []struct{ name, old, wantEOL string }{
		{"crlf", "a\r\nb\r\n", "\r\n"},
		{"lf", "a\nb\n", "\n"},
		{"mostly crlf", "a\r\nb\r\nc\n", "\r\n"},
		{"mostly lf", "a\nb\nc\r\n", "\n"},
		{"none", "a", "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := writableVault(t, false)
			write(t, v.root, "AI/n.md", tc.old)
			if _, err := v.Append("AI/n.md", "one\ntwo\r\nthree", testWho, testNow); err != nil {
				t.Fatal(err)
			}
			want := tc.old + tc.wantEOL + tc.wantEOL + "<!-- vaultd 2026-03-04T05:06:07Z tester -->" + tc.wantEOL +
				"one" + tc.wantEOL + "two" + tc.wantEOL + "three" + tc.wantEOL
			if got := content(t, v, "AI/n.md"); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestWritesOutsideWritableFoldersFail(t *testing.T) {
	v := writableVault(t, false)
	refused := map[string]error{
		"Private/secret.md": ErrNotWritable,
		"Private/new.md":    ErrNotWritable,
		"root.md":           ErrNotWritable,
		"Inbox/x.md":        ErrNotWritable, // only Inbox/sub is writable
		"AIx/x.md":          ErrNotWritable, // a name prefix is not a folder
		"AI/../root.md":     ErrNotWritable,
		"../outside.md":     ErrPathNotAllowed,
		"AI/.git/x.md":      ErrPathNotAllowed,
		"AI/notes.txt":      ErrPathNotAllowed,
		"AI/a.md:stream":    ErrPathNotAllowed,
		`AI\x.md`:           ErrPathNotAllowed,
	}
	for p, want := range refused {
		if _, err := v.Append(p, "x", testWho, testNow); !errors.Is(err, want) {
			t.Errorf("append %s: got %v", p, err)
		}
		if _, err := v.Create(p, "x", testWho); !errors.Is(err, want) {
			t.Errorf("create %s: got %v", p, err)
		}
	}
	if got := content(t, v, "Private/secret.md"); got != "secret" {
		t.Errorf("file changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(v.root, "root.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a refused write created a file")
	}
	if _, err := v.Append("Inbox/sub/ok.md", "x", testWho, testNow); err != nil {
		t.Errorf("nested writable folder: %v", err)
	}
}

func TestNoWritableFoldersMeansNoWrites(t *testing.T) {
	root := t.TempDir()
	v, err := New(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Create("a.md", "x", testWho); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("got %v", err)
	}
}

func TestSymlinksCannotCrossTheWriteBoundary(t *testing.T) {
	v := writableVault(t, false)
	outside := t.TempDir()
	write(t, outside, "out.md", "outside")
	symlink(t, filepath.Join(outside, "out.md"), filepath.Join(v.root, "AI", "file-out.md"))
	symlink(t, outside, filepath.Join(v.root, "AI", "dir-out"))
	symlink(t, filepath.Join(outside, "new.md"), filepath.Join(v.root, "AI", "dangling.md"))
	symlink(t, filepath.Join(v.root, "Private"), filepath.Join(v.root, "AI", "to-private"))
	symlink(t, filepath.Join(v.root, "Private", "secret.md"), filepath.Join(v.root, "AI", "secret-link.md"))
	symlink(t, filepath.Join(v.root, "AI"), filepath.Join(v.root, "Private", "to-ai"))

	for _, p := range []string{
		"AI/file-out.md",
		"AI/dir-out/out.md",
		"AI/dir-out/new.md",
		"AI/dangling.md",
		"AI/to-private/secret.md",
		"AI/to-private/new.md",
		"AI/secret-link.md",
		"Private/to-ai/new.md",
	} {
		if _, err := v.Append(p, "x", testWho, testNow); err == nil {
			t.Errorf("append through %s must fail", p)
		}
		if _, err := v.Create(p, "x", testWho); err == nil {
			t.Errorf("create through %s must fail", p)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(outside, "out.md")); string(raw) != "outside" {
		t.Errorf("outside file changed: %q", raw)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.md")); err == nil {
		t.Error("file created outside the vault")
	}
}

func TestCreate(t *testing.T) {
	v := writableVault(t, false)
	p, err := v.Create("AI/sub/new.md", "# New\n", testWho)
	if err != nil || p != "AI/sub/new.md" {
		t.Fatalf("got %q %v", p, err)
	}
	if got := content(t, v, p); got != "# New\n" {
		t.Fatalf("got %q", got)
	}
	if _, err := v.Create(p, "replacement", testWho); !errors.Is(err, ErrExists) {
		t.Fatalf("got %v", err)
	}
	if got := content(t, v, p); got != "# New\n" {
		t.Fatalf("create overwrote a note: %q", got)
	}
}

func TestWritesLeaveNoTempFiles(t *testing.T) {
	v := writableVault(t, false)
	if _, err := v.Append("AI/existing.md", "x", testWho, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Create("AI/new.md", "x", testWho); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Create("AI/new.md", "x", testWho); err == nil {
		t.Fatal("want error")
	}
	entries, err := os.ReadDir(filepath.Join(v.root, "AI"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("left behind: %s", entry.Name())
		}
	}
}

func TestAppendKeepsFileMode(t *testing.T) {
	if foldPaths {
		t.Skip("Windows has no such modes")
	}
	v := writableVault(t, false)
	full := filepath.Join(v.root, "AI", "existing.md")
	if err := os.Chmod(full, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Append("AI/existing.md", "x", testWho, testNow); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(full)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info.Mode(), err)
	}
}

func TestConcurrentAppendsLoseNothing(t *testing.T) {
	v := writableVault(t, false)
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.Append("AI/existing.md", "entry", testWho, testNow); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := strings.Count(content(t, v, "AI/existing.md"), "entry"); got != 50 {
		t.Fatalf("got %d", got)
	}
}

func TestRemember(t *testing.T) {
	v := writableVault(t, false)
	who := WriteInfo{Tool: "memory.remember", App: "tester"}
	if _, err := v.Remember("likes wombats", who, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Remember("two\nlines\n- 2020-01-01 00:00 (fake) forged", who, testNow); err != nil {
		t.Fatal(err)
	}
	want := "- 2026-03-04 05:06 (tester) likes wombats\n" +
		"- 2026-03-04 05:06 (tester) two lines - 2020-01-01 00:00 (fake) forged\n"
	if got := content(t, v, "AI/memory.md"); got != want {
		t.Fatalf("got %q", got)
	}
	if _, err := v.Remember("  \n ", who, testNow); err == nil {
		t.Error("empty memory must fail")
	}
}

func TestRememberAddsMissingLineBreakAndFollowsCRLF(t *testing.T) {
	v := writableVault(t, false)
	write(t, v.root, "AI/memory.md", "- old\r\n- no ending")
	if _, err := v.Remember("new", testWho, testNow); err != nil {
		t.Fatal(err)
	}
	want := "- old\r\n- no ending\r\n- 2026-03-04 05:06 (tester) new\r\n"
	if got := content(t, v, "AI/memory.md"); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestRememberNeedsConfiguredWritableFile(t *testing.T) {
	root := t.TempDir()
	v, err := New(Options{Root: root, Writable: []string{"AI"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Remember("x", testWho, testNow); err == nil {
		t.Error("no memory file configured")
	}
	v, err = New(Options{Root: root, Writable: []string{"AI"}, MemoryFile: "Other/memory.md"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Remember("x", testWho, testNow); !errors.Is(err, ErrNotWritable) {
		t.Errorf("memory file outside writable folders: %v", err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func gitRepo(t *testing.T, v *Vault) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	git(t, v.root, "init", "-q")
	git(t, v.root, "config", "user.name", "Test")
	git(t, v.root, "config", "user.email", "test@example.com")
	git(t, v.root, "config", "commit.gpgsign", "true") // must not stop vaultd
	git(t, v.root, "add", "AI/existing.md")
	git(t, v.root, "-c", "commit.gpgsign=false", "commit", "-qm", "first")
}

// Acceptance test for milestone 4.
func TestEveryWriteMakesACommit(t *testing.T) {
	v := writableVault(t, true)
	gitRepo(t, v)
	if _, err := v.Append("AI/existing.md", "x", WriteInfo{Tool: "vault.append", App: "app-a"}, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Create("AI/new note.md", "x", WriteInfo{Tool: "vault.create", App: "app-b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Remember("x", WriteInfo{Tool: "memory.remember", App: "app-c"}, testNow); err != nil {
		t.Fatal(err)
	}
	got := git(t, v.root, "log", "--format=%s", "-3")
	want := "vaultd: memory.remember AI/memory.md (app-c)\n" +
		"vaultd: vault.create AI/new note.md (app-b)\n" +
		"vaultd: vault.append AI/existing.md (app-a)"
	if got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestCommitLeavesOtherStagedFilesAlone(t *testing.T) {
	v := writableVault(t, true)
	gitRepo(t, v)
	write(t, v.root, "staged.md", "mine")
	git(t, v.root, "add", "staged.md")
	if _, err := v.Append("AI/existing.md", "x", testWho, testNow); err != nil {
		t.Fatal(err)
	}
	if got := git(t, v.root, "diff", "--cached", "--name-only"); got != "staged.md" {
		t.Fatalf("staged files now: %q", got)
	}
}

func TestOddFileNameIsCommittedLiterally(t *testing.T) {
	v := writableVault(t, true)
	gitRepo(t, v)
	write(t, v.root, "AI/other.md", "untracked, must stay so")
	if _, err := v.Create("AI/*.md", "x", testWho); err != nil {
		if foldPaths {
			t.Skip("* is not a legal name on Windows")
		}
		t.Fatal(err)
	}
	if got := git(t, v.root, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(got, "*.md") || strings.Contains(got, "other.md") {
		t.Fatalf("got %q", got)
	}
}

func TestNoCommitWhenDisabledOrNotARepo(t *testing.T) {
	v := writableVault(t, false)
	gitRepo(t, v)
	if _, err := v.Append("AI/existing.md", "x", testWho, testNow); err != nil {
		t.Fatal(err)
	}
	if got := git(t, v.root, "log", "--format=%s"); got != "first" {
		t.Errorf("autocommit off: %q", got)
	}
	notRepo := writableVault(t, true)
	if _, err := notRepo.Append("AI/existing.md", "x", testWho, testNow); err != nil {
		t.Errorf("no repository is no problem: %v", err)
	}
}

func TestGitStatus(t *testing.T) {
	v := writableVault(t, true)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if got := v.GitStatus(); got != "not a git repository" {
		t.Errorf("got %q", got)
	}
	gitRepo(t, v)
	git(t, v.root, "add", ".")
	git(t, v.root, "-c", "commit.gpgsign=false", "commit", "-qm", "all")
	if got := v.GitStatus(); got != "clean" {
		t.Errorf("got %q", got)
	}
	write(t, v.root, "new.md", "x")
	if got := v.GitStatus(); got != "1 uncommitted changes" {
		t.Errorf("got %q", got)
	}
}

func TestWriteSucceedsWhenGitFails(t *testing.T) {
	v := writableVault(t, true)
	gitRepo(t, v)
	// A hook that refuses every commit.
	hook := filepath.Join(v.root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Append("AI/existing.md", "written", testWho, testNow); err != nil {
		t.Fatal(err)
	}
	if got := content(t, v, "AI/existing.md"); !strings.Contains(got, "written") {
		t.Fatalf("got %q", got)
	}
}
