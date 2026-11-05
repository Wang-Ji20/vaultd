package index

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/hortensia/vaultd/internal/vault"
)

var ctx = context.Background()

func put(t testing.TB, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openTest(t testing.TB, root string) *Index {
	t.Helper()
	v, err := vault.New(vault.Options{Root: root, Ignore: []string{".git"}})
	if err != nil {
		t.Fatal(err)
	}
	ix, err := Open(filepath.Join(t.TempDir(), "data", "index.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix
}

func paths(hits []vault.Hit) string {
	var out []string
	for _, hit := range hits {
		out = append(out, hit.Path)
	}
	return strings.Join(out, ",")
}

func search(t testing.TB, ix *Index, text string) string {
	t.Helper()
	hits, err := ix.Search(ctx, text, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	return paths(hits)
}

func TestEscapeQuery(t *testing.T) {
	cases := map[string]string{
		"":                ``,
		"   ":             ``,
		"word":            `"word"`,
		"two words":       `"two" "words"`,
		`say "hi"`:        `"say" """hi"""`,
		"a AND b":         `"a" "AND" "b"`,
		"title:x":         `"title:x"`,
		"pre*":            `"pre*"`,
		"NEAR(a b)":       `"NEAR(a" "b)"`,
		"-not ^start":     `"-not" "^start"`,
		"nul\x00byte":     `"nul" "byte"`,
		"tab\tand\nlines": `"tab" "and" "lines"`,
	}
	for input, want := range cases {
		if got := EscapeQuery(input); got != want {
			t.Errorf("EscapeQuery(%q) = %q, want %q", input, got, want)
		}
	}
}

// No input may reach FTS5 as syntax, so no input may cause a query error.
func TestSearchAcceptsAnyText(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.md", "wombat")
	ix := openTest(t, root)
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{`"`, `""`, `AND`, `a OR`, `(`, `NOT x`, `*`, `title:wombat`, `{a}`, `--`, `wombat --`, "é", "日本語", `'; DROP TABLE notes; --`} {
		if _, err := ix.Search(ctx, text, 5, ""); err != nil {
			t.Errorf("%q: %v", text, err)
		}
	}
	property := func(text string) bool {
		if EscapeQuery(text) == "" {
			return true
		}
		_, err := ix.Search(ctx, text, 5, "")
		return err == nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
	if got := search(t, ix, "wombat"); got != "a.md" {
		t.Errorf("table must survive: %q", got)
	}
}

func TestQuerySyntaxIsNotInterpreted(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.md", "wombat")
	put(t, root, "b.md", "numbat")
	ix := openTest(t, root)
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := search(t, ix, "wombat OR numbat"); got != "" {
		t.Errorf("OR must be a plain word: %q", got)
	}
	if got := search(t, ix, "wom*"); got != "" {
		t.Errorf("* must not be a prefix operator: %q", got)
	}
	if got := search(t, ix, "WOMBAT"); got != "a.md" {
		t.Errorf("search ignores case: %q", got)
	}
}

// Regression test: bm25 weights go by column and path is the first column.
// Without a weight for path, tags get 1 instead of 5, so three body matches
// (a score of 3) would beat one tag match (5 with the right weights).
func TestRankingPrefersTitleThenTagsThenBody(t *testing.T) {
	root := t.TempDir()
	filler := strings.Repeat("filler words go here ", 10)
	put(t, root, "body.md", "# Other\n"+filler+"wombat wombat wombat "+filler)
	put(t, root, "tags.md", "---\ntags: [wombat]\n---\n# Other\n"+filler)
	put(t, root, "title.md", "---\ntitle: wombat\n---\n"+filler)
	ix := openTest(t, root)
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := search(t, ix, "wombat"); got != "title.md,tags.md,body.md" {
		t.Fatalf("got %s", got)
	}
	hits, err := ix.Search(ctx, "wombat", 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if hits[0].Score <= hits[1].Score || hits[1].Score <= hits[2].Score || hits[2].Score <= 0 {
		t.Errorf("scores should be positive and fall: %+v", hits)
	}
}

func TestSearchResultFields(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.md", "---\ntitle: T\ntags: [x]\n---\nthe wombat digs\n")
	ix := openTest(t, root)
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	hits, err := ix.Search(ctx, "wombat", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	hit := hits[0]
	if hit.Title != "T" || hit.Tags[0] != "x" || !strings.Contains(hit.Snippet, "**wombat**") || hit.Modified == "" {
		t.Fatalf("got %+v", hit)
	}
}

func TestSearchFolderAndLimit(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.md", "wombat")
	put(t, root, "sub/b.md", "wombat")
	put(t, root, "subway/c.md", "wombat")
	ix := openTest(t, root)
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	hits, err := ix.Search(ctx, "wombat", 50, "sub")
	if err != nil || paths(hits) != "sub/b.md" {
		t.Errorf("folder: %q %v", paths(hits), err)
	}
	if hits, _ := ix.Search(ctx, "wombat", 2, ""); len(hits) != 2 {
		t.Errorf("limit: %d", len(hits))
	}
	if _, err := ix.Search(ctx, "wombat", 5, "../x"); err == nil {
		t.Error("folder must pass the jail's text checks")
	}
	if _, err := ix.Search(ctx, " ", 5, ""); err == nil {
		t.Error("empty query must fail")
	}
}

func TestSyncDetectsChanges(t *testing.T) {
	root := t.TempDir()
	put(t, root, "keep.md", "alpha")
	put(t, root, "change.md", "beta")
	put(t, root, "gone.md", "gamma")
	put(t, root, ".git/skip.md", "delta")
	put(t, root, "skip.txt", "delta")
	ix := openTest(t, root)
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if state := ix.State(); state.Notes != 3 || state.Indexing || state.Done != 3 || state.Total != 3 {
		t.Fatalf("state %+v", state)
	}
	if got := search(t, ix, "delta"); got != "" {
		t.Errorf("ignored files must not be indexed: %q", got)
	}
	put(t, root, "change.md", "betatwo")
	if err := os.Remove(filepath.Join(root, "gone.md")); err != nil {
		t.Fatal(err)
	}
	put(t, root, "new.md", "epsilon")
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for text, want := range map[string]string{"alpha": "keep.md", "beta": "", "betatwo": "change.md", "gamma": "", "epsilon": "new.md"} {
		if got := search(t, ix, text); got != want {
			t.Errorf("%s: got %q, want %q", text, got, want)
		}
	}
}

func TestSyncSkipsUnchangedAndRefreshesTime(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.md", "alpha")
	ix := openTest(t, root)
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "a.md"), later, later); err != nil {
		t.Fatal(err)
	}
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	hits, err := ix.Search(ctx, "alpha", 1, "")
	if err != nil || hits[0].Modified != vault.FormatTime(later) {
		t.Fatalf("time not refreshed: %+v %v", hits, err)
	}
}

func TestRebuildAndReopen(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.md", "alpha [[b]]")
	put(t, root, "b.md", "beta")
	ix := openTest(t, root)
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ix.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if got := search(t, ix, "alpha"); got != "a.md" {
		t.Fatalf("after rebuild: %q", got)
	}
	links, err := ix.Backlinks(ctx, "b.md")
	if err != nil || len(links) != 1 {
		t.Fatalf("links must not double: %+v %v", links, err)
	}
}

func TestOversizedNoteIsListedNotRead(t *testing.T) {
	root := t.TempDir()
	put(t, root, "big.md", "wombat "+strings.Repeat("x", vault.MaxReadSize))
	ix := openTest(t, root)
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if state := ix.State(); state.Notes != 1 {
		t.Fatalf("state %+v", state)
	}
	if got := search(t, ix, "big"); got != "big.md" {
		t.Fatalf("name should still be searchable: %q", got)
	}
}

func TestBacklinks(t *testing.T) {
	root := t.TempDir()
	put(t, root, "target.md", "# Target\n[[target]] self link\n")
	put(t, root, "dir/target.md", "another with a longer path\n")
	put(t, root, "one.md", "# One\nfirst [[Target]]\nsecond [[Target#h|x]]\n")
	put(t, root, "two.md", "explicit [[dir/target]]\n")
	put(t, root, "three.md", "unrelated [[missing]]\n")
	ix := openTest(t, root)
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	links, err := ix.Backlinks(ctx, "target.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 || links[0].Path != "one.md" || links[0].Title != "One" || links[0].Context != "first [[Target]]" {
		t.Fatalf("got %+v", links)
	}
	links, err = ix.Backlinks(ctx, "dir/target.md")
	if err != nil || len(links) != 1 || links[0].Path != "two.md" {
		t.Fatalf("got %+v %v", links, err)
	}
}

// A link resolves to the note that is currently the best match.
func TestBacklinksFollowNewBetterMatch(t *testing.T) {
	root := t.TempDir()
	put(t, root, "deep/down/Note.md", "x")
	put(t, root, "src.md", "[[Note]]")
	ix := openTest(t, root)
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if links, _ := ix.Backlinks(ctx, "deep/down/Note.md"); len(links) != 1 {
		t.Fatalf("got %+v", links)
	}
	put(t, root, "Note.md", "y")
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if links, _ := ix.Backlinks(ctx, "deep/down/Note.md"); len(links) != 0 {
		t.Fatalf("the shorter path should take over: %+v", links)
	}
	if links, _ := ix.Backlinks(ctx, "Note.md"); len(links) != 1 {
		t.Fatalf("got %+v", links)
	}
}

func TestLinkCandidates(t *testing.T) {
	got := strings.Join(linkCandidates("A/B/Note.md"), ",")
	if got != "a/b/note,b/note,note" {
		t.Fatal(got)
	}
}

func TestOpenReplacesOldSchema(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.md", "alpha")
	v, err := vault.New(vault.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "index.db")
	ix, err := Open(dbPath, v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.db.Exec(`PRAGMA user_version = 0; DROP TABLE links; CREATE TABLE links (old TEXT)`); err != nil {
		t.Fatal(err)
	}
	ix.Close()
	ix, err = Open(dbPath, v)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, limit time.Duration, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: not within %v", what, limit)
}

func startWatching(t *testing.T, ix *Index) {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	done, err := ix.Start(runCtx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, 5*time.Second, "first sync", func() bool { return !ix.State().Indexing })
}

func TestWatcherSeesChanges(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.md", "alpha")
	put(t, root, ".git/x.md", "ignored")
	ix := openTest(t, root)
	startWatching(t, ix)
	found := func(text, want string) func() bool {
		return func() bool { return search(t, ix, text) == want }
	}
	const limit = 2 * time.Second

	put(t, root, "a.md", "modified")
	waitFor(t, limit, "modify", found("modified", "a.md"))
	if got := search(t, ix, "alpha"); got != "" {
		t.Errorf("old text still found: %q", got)
	}

	put(t, root, "new.md", "created")
	waitFor(t, limit, "create", found("created", "new.md"))

	// A new folder, then a note in it straight away.
	put(t, root, "fresh/deep/note.md", "infolder")
	waitFor(t, limit, "new folder", found("infolder", "fresh/deep/note.md"))
	put(t, root, "fresh/deep/later.md", "afterwards")
	waitFor(t, limit, "file in new folder", found("afterwards", "fresh/deep/later.md"))

	// Rename over, the way many editors save.
	put(t, root, "a.md.tmp", "renamed")
	if err := os.Rename(filepath.Join(root, "a.md.tmp"), filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, limit, "rename over", found("renamed", "a.md"))

	// Delete then create.
	if err := os.Remove(filepath.Join(root, "new.md")); err != nil {
		t.Fatal(err)
	}
	put(t, root, "new.md", "recreated")
	waitFor(t, limit, "delete and create", found("recreated", "new.md"))

	if err := os.Remove(filepath.Join(root, "new.md")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, limit, "delete", found("recreated", ""))

	// Moving a folder out removes its notes; moving it back brings them back.
	if err := os.Rename(filepath.Join(root, "fresh"), filepath.Join(filepath.Dir(root), filepath.Base(root)+"-moved")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root + "-moved") })
	waitFor(t, limit, "folder moved out", found("infolder", ""))
	if err := os.Rename(root+"-moved", filepath.Join(root, "back")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, limit, "folder moved in", found("infolder", "back/deep/note.md"))

	put(t, root, ".git/x.md", "stillignored")
	time.Sleep(700 * time.Millisecond)
	if got := search(t, ix, "stillignored"); got != "" {
		t.Errorf("ignored folder indexed: %q", got)
	}
}

// Search must answer while the first sync is still running.
func TestSearchServesDuringIndexing(t *testing.T) {
	root := t.TempDir()
	for i := range 3000 {
		put(t, root, fmt.Sprintf("n%04d.md", i), "wombat note")
	}
	ix := openTest(t, root)
	startCtx, cancel := context.WithCancel(ctx)
	done, err := ix.Start(startCtx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); <-done }()
	if !ix.State().Indexing {
		t.Error("indexing must be reported from the start")
	}
	for ix.State().Indexing {
		if _, err := ix.Search(ctx, "wombat", 5, ""); err != nil {
			t.Fatal(err)
		}
	}
	if state := ix.State(); state.Notes != 3000 {
		t.Fatalf("state %+v", state)
	}
}

// Acceptance test for milestone 2. See docs/perf/search-index.md.
func TestIndexes20000NotesWithinOneMinute(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	root := t.TempDir()
	generate20000(t, root)
	ix := openTest(t, root)
	start := time.Now()
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("indexed 20000 notes in %v", elapsed)
	if elapsed > time.Minute {
		t.Fatalf("took %v", elapsed)
	}
	if state := ix.State(); state.Notes != 20000 {
		t.Fatalf("state %+v", state)
	}
	start = time.Now()
	if got := search(t, ix, "needle12345"); got != "f12/n12345.md" {
		t.Fatalf("got %q", got)
	}
	t.Logf("search took %v", time.Since(start))
	start = time.Now()
	if err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("unchanged resync took %v", time.Since(start))
}

func generate20000(t testing.TB, root string) {
	t.Helper()
	body := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", 40)
	for i := range 20000 {
		note := fmt.Sprintf("---\ntags: [t%d]\n---\n# Note %d\nneedle%d %s\n[[n%05d]]\n", i%50, i, i, body, (i+1)%20000)
		put(t, root, fmt.Sprintf("f%02d/n%05d.md", i/1000, i), note)
	}
}
