package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleLog(t *testing.T) *Logger {
	t.Helper()
	l, _ := openLog(t, Settings{MaxSizeMB: 1, MaxFiles: 3, LogArguments: true})
	calls := []Call{
		{App: "a", Tool: "vault.read", Args: []byte(`{"path":"First.md"}`)},
		{App: "b", Tool: "vault.read", Err: "not found"},
		{App: "a", Tool: "fake.echo"},
	}
	for i, call := range calls {
		if err := l.Record(call, testTime.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func TestQueryFilters(t *testing.T) {
	l := sampleLog(t)
	yes, no := true, false
	cases := []struct {
		name   string
		filter Filter
		tools  string
	}{
		{"all, newest first", Filter{}, "fake.echo vault.read vault.read "},
		{"app", Filter{App: "b"}, "vault.read "},
		{"tool", Filter{Tool: "fake.echo"}, "fake.echo "},
		{"ok", Filter{OK: &yes}, "fake.echo vault.read "},
		{"error", Filter{OK: &no}, "vault.read "},
		{"since", Filter{Since: testTime.Add(90 * time.Minute)}, "fake.echo "},
		{"until", Filter{Until: testTime.Add(30 * time.Minute)}, "vault.read "},
		{"text ignores case", Filter{Text: "FIRST.MD"}, "vault.read "},
		{"nothing", Filter{Text: "absent"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := l.Query(tc.filter, 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			for _, row := range page.Rows {
				got += row.Tool + " "
			}
			if got != tc.tools {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestQueryPagesAcrossRotatedFiles(t *testing.T) {
	l, dir := openLog(t, Settings{MaxSizeMB: 1, MaxFiles: 3, LogArguments: true})
	for i := range 6000 {
		call := Call{App: "a", Tool: fmt.Sprintf("t%d", i), ResultBytes: i}
		if err := l.Record(call, testTime); err != nil {
			t.Fatal(err)
		}
		if i%3000 == 2999 {
			if err := l.rotate(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, fileName+".1")); err != nil {
		t.Fatal(err)
	}
	first, err := l.Query(Filter{}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != 50 || !first.HasMore || first.Rows[0].Tool != "t5999" || first.Rows[49].Tool != "t5950" {
		t.Fatalf("first page: %d rows, first %s", len(first.Rows), first.Rows[0].Tool)
	}
	// A page crossing the boundary between two files.
	across, err := l.Query(Filter{}, 2990, 20)
	if err != nil {
		t.Fatal(err)
	}
	if across.Rows[0].Tool != "t3009" || across.Rows[19].Tool != "t2990" {
		t.Fatalf("got %s..%s", across.Rows[0].Tool, across.Rows[19].Tool)
	}
	last, err := l.Query(Filter{}, 5990, 50)
	if err != nil || len(last.Rows) != 10 || last.HasMore {
		t.Fatalf("last page: %d rows, more=%v, %v", len(last.Rows), last.HasMore, err)
	}
}

func TestQueryRowKeepsFullLine(t *testing.T) {
	l := sampleLog(t)
	page, err := l.Query(Filter{App: "b"}, 0, 1)
	if err != nil || page.Rows[0].Raw == "" || page.Rows[0].Error != "not found" {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestQuerySkipsTornLines(t *testing.T) {
	l, dir := openLog(t, Settings{MaxSizeMB: 1, MaxFiles: 2})
	if err := os.WriteFile(filepath.Join(dir, fileName+".1"), []byte("{broken\n{\"tool\":\"old\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := l.Query(Filter{}, 0, 50)
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Tool != "old" {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestStats(t *testing.T) {
	l := sampleLog(t)
	stats, err := l.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if a := stats["a"]; a.Calls != 2 || a.LastSeen != "2026-01-02T05:04:05Z" {
		t.Errorf("got %+v", a)
	}
	if b := stats["b"]; b.Calls != 1 {
		t.Errorf("got %+v", b)
	}
}
