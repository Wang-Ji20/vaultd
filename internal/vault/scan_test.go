package vault

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecent(t *testing.T) {
	v := sampleVault(t)
	now := time.Now()
	age := func(rel string, ago time.Duration) {
		if err := os.Chtimes(filepath.Join(v.root, filepath.FromSlash(rel)), now, now.Add(-ago)); err != nil {
			t.Fatal(err)
		}
	}
	age("a.md", time.Hour)
	age("b.md", 2*time.Hour)
	age("sub/deep.md", 10*24*time.Hour)

	got, err := v.Recent(7, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != "a.md" || got[0].Title != "Ant" || got[1].Path != "b.md" {
		t.Fatalf("got %+v", got)
	}
	if got, _ := v.Recent(7, 1, now); len(got) != 1 {
		t.Errorf("limit: %+v", got)
	}
	if got, _ := v.Recent(30, 20, now); len(got) != 3 {
		t.Errorf("days: %+v", got)
	}
}

func TestReadRaw(t *testing.T) {
	v := sampleVault(t)
	raw, info, err := v.ReadRaw("b.md", 1000)
	if err != nil || len(raw) == 0 || info == nil {
		t.Fatalf("got %q %v %v", raw, info, err)
	}
	raw, info, err = v.ReadRaw("b.md", 1)
	if err != nil || raw != nil || info == nil {
		t.Fatalf("oversized: %q %v %v", raw, info, err)
	}
	if _, _, err := v.ReadRaw("../x.md", 1000); !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("jail: %v", err)
	}
}

func TestRel(t *testing.T) {
	v := sampleVault(t)
	if rel, ok := v.Rel(filepath.Join(v.root, "sub", "deep.md")); !ok || rel != "sub/deep.md" {
		t.Errorf("got %q %v", rel, ok)
	}
	for _, abs := range []string{v.root, filepath.Join(v.root, ".git", "x.md"), filepath.Dir(v.root)} {
		if rel, ok := v.Rel(abs); ok {
			t.Errorf("%s: got %q", abs, rel)
		}
	}
}

func TestWalkFoldersSkipsIgnored(t *testing.T) {
	v := sampleVault(t)
	var seen []string
	if err := v.WalkFolders(".", func(abs string) error {
		seen = append(seen, abs)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != v.root || seen[1] != filepath.Join(v.root, "sub") {
		t.Fatalf("got %v", seen)
	}
}
