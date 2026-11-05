// Package vault gives safe access to an Obsidian vault on disk.
package vault

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Parsed is what vaultd understands about a note's text.
type Parsed struct {
	Frontmatter map[string]any
	Tags        []string
	Title       string
	Body        string
	Links       []Link
}

// Link is a wikilink found in a note.
type Link struct {
	Target  string // note name or path, without heading, alias or .md
	Heading string
	Alias   string
	Line    string // the whole line holding the link
}

var (
	headingRE    = regexp.MustCompile(`^ {0,3}#{1,6}(\s|$)`)
	titleRE      = regexp.MustCompile(`^# +(.+?)\s*$`)
	inlineCodeRE = regexp.MustCompile("`[^`]*`")
	tagRE        = regexp.MustCompile(`(?:^|\s)#([\p{L}\p{N}_/-]+)`)
	wikilinkRE   = regexp.MustCompile(`\[\[([^\[\]\n]+?)\]\]`)
	fenceRE      = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
)

// Parse reads a note's text. name is its file name, used as the fallback
// title. It never fails: a note with broken frontmatter is still a note.
func Parse(name string, raw []byte) Parsed {
	text := strings.TrimPrefix(string(raw), "\xef\xbb\xbf")
	yamlText, body := splitFrontmatter(text)
	p := Parsed{Frontmatter: parseFrontmatter(yamlText), Body: body, Links: []Link{}}

	tags := frontmatterTags(p.Frontmatter["tags"])
	heading := ""
	scanBody(body, func(line string, code bool) {
		if code {
			return
		}
		visible := inlineCodeRE.ReplaceAllStringFunc(line, blank)
		if headingRE.MatchString(visible) {
			if m := titleRE.FindStringSubmatch(visible); m != nil && heading == "" {
				heading = m[1]
			}
		} else {
			tags = append(tags, inlineTags(visible)...)
		}
		p.Links = append(p.Links, parseLinks(visible, line)...)
	})
	p.Tags = dedupeFold(tags)
	p.Title = chooseTitle(p.Frontmatter, heading, name)
	return p
}

func blank(s string) string { return strings.Repeat(" ", len(s)) }

// splitFrontmatter separates a leading YAML block from the body.
func splitFrontmatter(text string) (yamlText, body string) {
	first, rest, found := strings.Cut(text, "\n")
	if !found || strings.TrimRight(first, " \t\r") != "---" {
		return "", text
	}
	offset := 0
	for offset <= len(rest) {
		line, after, more := strings.Cut(rest[offset:], "\n")
		if trimmed := strings.TrimRight(line, " \t\r"); trimmed == "---" || trimmed == "..." {
			return rest[:offset], after
		}
		if !more {
			break
		}
		offset += len(line) + 1
	}
	return "", text // no closing line: this was just a horizontal rule
}

func parseFrontmatter(yamlText string) map[string]any {
	out := map[string]any{}
	var decoded map[string]any
	if err := yaml.Unmarshal([]byte(yamlText), &decoded); err != nil {
		return out
	}
	for key, value := range decoded {
		out[key] = jsonSafe(value)
	}
	return out
}

// jsonSafe rewrites YAML values that JSON cannot carry: maps with non-string
// keys, and the special numbers .nan and .inf.
func jsonSafe(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = jsonSafe(item)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[fmt.Sprint(key)] = jsonSafe(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = jsonSafe(item)
		}
		return out
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Sprint(v)
		}
	}
	return value
}

func frontmatterTags(value any) []string {
	var tags []string
	switch v := value.(type) {
	case string:
		tags = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				tags = append(tags, s)
			}
		}
	}
	for i, tag := range tags {
		tags[i] = strings.TrimPrefix(strings.TrimSpace(tag), "#")
	}
	return slices.DeleteFunc(tags, func(tag string) bool { return tag == "" })
}

// inlineTags finds #tags. As in Obsidian, a tag needs a non-digit, so "#123"
// is an issue number, not a tag.
func inlineTags(line string) []string {
	var tags []string
	for _, m := range tagRE.FindAllStringSubmatch(line, -1) {
		if strings.ContainsFunc(m[1], func(r rune) bool { return !unicode.IsDigit(r) }) {
			tags = append(tags, m[1])
		}
	}
	return tags
}

func dedupeFold(items []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, item := range items {
		key := strings.ToLower(item)
		if !seen[key] {
			seen[key] = true
			out = append(out, item)
		}
	}
	return out
}

// scanBody calls visit for each line and says whether it is inside a fenced
// or indented code block.
func scanBody(body string, visit func(line string, code bool)) {
	fence := ""
	previousBlank := true
	inIndented := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			visit(line, true)
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if m := fenceRE.FindStringSubmatch(line); m != nil {
			fence = m[1]
			visit(line, true)
			continue
		}
		indented := strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
		inIndented = trimmed != "" && indented && (previousBlank || inIndented)
		visit(line, inIndented)
		previousBlank = trimmed == ""
	}
}

func parseLinks(visible, line string) []Link {
	var links []Link
	for _, m := range wikilinkRE.FindAllStringSubmatch(visible, -1) {
		if link, ok := parseLink(m[1]); ok {
			link.Line = line
			links = append(links, link)
		}
	}
	return links
}

// parseLink splits "Target#heading|alias". A link with no target points
// inside the same note, so it says nothing about other notes.
func parseLink(inner string) (Link, bool) {
	target, alias, _ := strings.Cut(inner, "|")
	target = strings.TrimSuffix(target, `\`) // "\|" escapes the bar in tables
	target, heading, _ := strings.Cut(target, "#")
	target = strings.TrimSpace(target)
	target = strings.TrimSuffix(target, ".md")
	if target == "" {
		return Link{}, false
	}
	return Link{Target: target, Heading: strings.TrimSpace(heading), Alias: strings.TrimSpace(alias)}, true
}

func chooseTitle(frontmatter map[string]any, heading, name string) string {
	if title, ok := frontmatter["title"].(string); ok && strings.TrimSpace(title) != "" {
		return strings.TrimSpace(title)
	}
	if heading != "" {
		return heading
	}
	return strings.TrimSuffix(path.Base(name), path.Ext(name))
}

// ResolveLink finds the note a wikilink target means, the way Obsidian does.
// A bare name matches by file name anywhere; a target with folders matches by
// path ending. Case is ignored. When several match, the shortest path wins,
// and among equals the alphabetically first, so the result is stable. It
// returns "" if nothing matches.
func ResolveLink(target string, notes []string) string {
	want := LinkKey(target)
	best := ""
	for _, note := range notes {
		key := LinkKey(note)
		if key != want && !strings.HasSuffix(key, "/"+want) {
			continue
		}
		if best == "" || len(note) < len(best) || (len(note) == len(best) && note < best) {
			best = note
		}
	}
	return best
}

// LinkKey is the normalized form used to compare link targets and note paths.
func LinkKey(p string) string {
	p = strings.ToLower(strings.TrimPrefix(p, "/"))
	return strings.TrimSuffix(path.Clean(p), ".md")
}
