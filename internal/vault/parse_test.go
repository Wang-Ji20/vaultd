package vault

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFrontmatter(t *testing.T) {
	note := "---\ntitle: Hello\nn: 3\nnested: {a: 1}\n---\nbody\n"
	p := Parse("x.md", []byte(note))
	if p.Frontmatter["title"] != "Hello" || p.Frontmatter["n"] != 3 {
		t.Fatalf("got %v", p.Frontmatter)
	}
	if p.Body != "body\n" {
		t.Fatalf("body %q", p.Body)
	}
}

func TestFrontmatterEdgeCases(t *testing.T) {
	cases := []struct {
		name, note, body string
		keys             int
	}{
		{"crlf", "---\r\na: 1\r\n---\r\nbody", "body", 1},
		{"bom", "\xef\xbb\xbf---\na: 1\n---\nbody", "body", 1},
		{"dots close", "---\na: 1\n...\nbody", "body", 1},
		{"no close", "---\nnot closed\n", "---\nnot closed\n", 0},
		{"not at top", "x\n---\na: 1\n---\n", "x\n---\na: 1\n---\n", 0},
		{"broken yaml", "---\n: : :\n  - [\n---\nbody", "body", 0},
		{"empty block", "---\n---\nbody", "body", 0},
		{"none", "just text", "just text", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Parse("x.md", []byte(tc.note))
			if p.Body != tc.body || len(p.Frontmatter) != tc.keys {
				t.Fatalf("body %q, frontmatter %v", p.Body, p.Frontmatter)
			}
		})
	}
}

func TestFrontmatterAlwaysEncodesAsJSON(t *testing.T) {
	note := "---\n1: numeric key\nnested: {2: x}\nlist: [{3: y}]\nnan: .nan\ninf: .inf\nday: 2024-01-02\n---\n"
	p := Parse("x.md", []byte(note))
	if _, err := json.Marshal(p.Frontmatter); err != nil {
		t.Fatalf("frontmatter must encode: %v (%v)", err, p.Frontmatter)
	}
}

func TestTags(t *testing.T) {
	cases := []struct {
		name, note string
		want       []string
	}{
		{"list", "---\ntags: [a, '#b']\n---\n", []string{"a", "b"}},
		{"block list", "---\ntags:\n  - a\n  - b/c\n---\n", []string{"a", "b/c"}},
		{"string", "---\ntags: a, b c\n---\n", []string{"a", "b", "c"}},
		{"non-string ignored", "---\ntags: [1, a]\n---\n", []string{"a"}},
		{"inline", "text #one and #two/sub.\n#start", []string{"one", "two/sub", "start"}},
		{"unicode", "#größe #日本語", []string{"größe", "日本語"}},
		{"both deduped", "---\ntags: [A]\n---\n#a #b", []string{"A", "b"}},
		{"heading", "# Not a tag\n## #nope\n", []string{}},
		{"tag-like start is a tag", "#notheading text", []string{"notheading"}},
		{"fenced", "```\n#no\n```\n#yes", []string{"yes"}},
		{"tilde fence", "~~~\n#no\n~~~\n#yes", []string{"yes"}},
		{"long fence", "````\n```\n#no\n````\n#yes", []string{"yes"}},
		{"unclosed fence", "```\n#no\n", []string{}},
		{"inline code", "`#no` #yes", []string{"yes"}},
		{"indented code", "text\n\n    #no\n\n#yes", []string{"yes"}},
		{"indented list item", "- a\n    - #yes", []string{"yes"}},
		{"numbers", "#123 #1a", []string{"1a"}},
		{"url fragment", "http://x.y/#frag a#b", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse("x.md", []byte(tc.note)).Tags; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTitle(t *testing.T) {
	cases := []struct{ name, note, want string }{
		{"frontmatter", "---\ntitle: FM\n---\n# H\n", "FM"},
		{"heading", "text\n# First\n# Second", "First"},
		{"trailing space", "#  Spaced  \n", "Spaced"},
		{"filename", "no heading", "My Note"},
		{"h2 is not title", "## Sub", "My Note"},
		{"heading in code", "```\n# No\n```\n", "My Note"},
		{"non-string title", "---\ntitle: 5\n---\n", "My Note"},
		{"blank title", "---\ntitle: ' '\n---\n# H", "H"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse("dir/My Note.md", []byte(tc.note)).Title; got != tc.want {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestWikilinks(t *testing.T) {
	note := "see [[Target]] and [[Other|alias]] then [[Third#Part]]\n" +
		"[[Dir/Fourth#H|A]] ![[Embed.md]] [[#local]]\n" +
		"| [[Table\\|cell]] |\n" +
		"`[[code]]`\n```\n[[fenced]]\n```\n"
	links := Parse("x.md", []byte(note)).Links
	want := []Link{
		{Target: "Target", Line: "see [[Target]] and [[Other|alias]] then [[Third#Part]]"},
		{Target: "Other", Alias: "alias", Line: "see [[Target]] and [[Other|alias]] then [[Third#Part]]"},
		{Target: "Third", Heading: "Part", Line: "see [[Target]] and [[Other|alias]] then [[Third#Part]]"},
		{Target: "Dir/Fourth", Heading: "H", Alias: "A", Line: "[[Dir/Fourth#H|A]] ![[Embed.md]] [[#local]]"},
		{Target: "Embed", Line: "[[Dir/Fourth#H|A]] ![[Embed.md]] [[#local]]"},
		{Target: "Table", Alias: "cell", Line: "| [[Table\\|cell]] |"},
	}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("got %+v", links)
	}
}

func TestResolveLink(t *testing.T) {
	notes := []string{"deep/down/Note.md", "b/Note.md", "a/Note.md", "x/Other.md", "Root.md", "y/x/Other.md"}
	cases := map[string]string{
		"Note":       "a/Note.md",
		"note":       "a/Note.md",
		"Note.md":    "a/Note.md",
		"down/Note":  "deep/down/Note.md",
		"b/Note":     "b/Note.md",
		"x/Other":    "x/Other.md",
		"/Root":      "Root.md",
		"Missing":    "",
		"ote":        "",
		"own/Note":   "",
		"deep/ /Not": "",
	}
	for target, want := range cases {
		if got := ResolveLink(target, notes); got != want {
			t.Errorf("ResolveLink(%q) = %q, want %q", target, got, want)
		}
	}
}
