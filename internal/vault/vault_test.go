package vault

import (
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

func sampleVault(t *testing.T) *Vault {
	t.Helper()
	root := t.TempDir()
	write(t, root, "b.md", "# Bee\nbuzz #insect\n")
	write(t, root, "a.md", "---\ntitle: Ant\ntags: [insect]\n---\nmarch\n")
	write(t, root, "notes.txt", "not a note")
	write(t, root, "sub/deep.md", "buzz buzz deep\n")
	write(t, root, ".git/x.md", "buzz secret\n")
	write(t, root, ".obsidian/y.md", "buzz secret\n")
	v, err := New(Options{Root: root, Ignore: []string{".git", ".obsidian"}})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRead(t *testing.T) {
	v := sampleVault(t)
	note, err := v.Read("a.md")
	if err != nil {
		t.Fatal(err)
	}
	if note.Title != "Ant" || note.Content != "march\n" || note.Tags[0] != "insect" || note.Size == 0 || note.Modified == "" {
		t.Fatalf("got %+v", note)
	}
}

func TestReadRefusals(t *testing.T) {
	v := sampleVault(t)
	write(t, v.root, "big.md", strings.Repeat("x", MaxReadSize+1))
	write(t, v.root, "edge.md", strings.Repeat("x", MaxReadSize))
	if _, err := v.Read("edge.md"); err != nil {
		t.Errorf("exactly 1 MB must be readable: %v", err)
	}
	if _, err := v.Read("big.md"); err == nil || !strings.Contains(err.Error(), "1 MB") {
		t.Errorf("big file: %v", err)
	}
	if _, err := v.Read("notes.txt"); !errors.Is(err, ErrPathNotAllowed) {
		t.Errorf("non-note: %v", err)
	}
	if _, err := v.Read("missing.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := v.Read("../x.md"); !errors.Is(err, ErrPathNotAllowed) {
		t.Errorf("escape: %v", err)
	}
	if _, err := v.Read(".git/x.md"); !errors.Is(err, ErrPathNotAllowed) {
		t.Errorf("ignored: %v", err)
	}
}

func TestErrorsHideAbsolutePaths(t *testing.T) {
	v := sampleVault(t)
	for _, p := range []string{"missing.md", "sub.md/x.md", "a.md/x.md"} {
		if _, err := v.Read(p); err == nil || strings.Contains(err.Error(), v.root) {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestList(t *testing.T) {
	v := sampleVault(t)
	got, err := v.List("")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Folders, []string{"sub"}) {
		t.Errorf("folders %v", got.Folders)
	}
	if len(got.Notes) != 2 || got.Notes[0].Path != "a.md" || got.Notes[0].Title != "Ant" || got.Notes[1].Path != "b.md" {
		t.Errorf("notes %+v", got.Notes)
	}
	got, err = v.List("sub")
	if err != nil || got.Notes[0].Path != "sub/deep.md" {
		t.Errorf("sub: %+v, %v", got, err)
	}
	if _, err := v.List(".git"); !errors.Is(err, ErrPathNotAllowed) {
		t.Errorf("ignored: %v", err)
	}
	if _, err := v.List("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

func TestListSkipsSymlinkedEntries(t *testing.T) {
	v := sampleVault(t)
	outside := t.TempDir()
	write(t, outside, "out.md", "secret")
	symlink(t, outside+"/out.md", v.root+"/link.md")
	symlink(t, outside, v.root+"/linkdir")
	got, err := v.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Notes) != 2 || len(got.Folders) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestWalkNotes(t *testing.T) {
	v := sampleVault(t)
	write(t, v.root, "bad:name.md", "x") // legal on Linux, refused by the jail
	var seen []string
	err := v.WalkNotes(func(rel string, _ fs.FileInfo) error {
		seen = append(seen, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.md", "b.md", "sub/deep.md"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("got %v", seen)
	}
}
