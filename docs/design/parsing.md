# Design: note parsing

`vault.Parse` turns a note's text into its frontmatter, tags, title and links.

## It never fails

A note with broken YAML is still a note. vaultd shows its body and an empty
frontmatter instead of hiding the file. A `---` block with no closing line is
treated as an ordinary horizontal rule.

## What counts as a tag

```
#one            tag
text #two/sub   tag
# heading       not a tag (a heading line)
`#code`         not a tag (inline code)
http://x/#frag  not a tag (no space before the #)
#123            not a tag (needs a non-digit, as in Obsidian)
```

Lines in fenced or indented code blocks are skipped. An indented line only counts
as code after a blank line, so nested list items are still read.

## Resolving links

`[[Note]]` matches a file name anywhere in the vault. `[[dir/Note]]` matches the
end of a path. Case is ignored. If several notes match, the shortest path wins,
then the alphabetically first, so the answer is always the same.

## JSON safety

YAML can hold values JSON cannot: maps with number keys, `.nan` and `.inf`. These
are converted, because a single odd note must not break a tool's response.
