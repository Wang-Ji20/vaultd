# Design: the path jail

Every path from a tool call comes from an AI model, which may be following
instructions hidden in a note. So every path is treated as hostile.

## The checks, in order

```
"a/../b.md"
    |
    v
1. Text checks      NUL, \, :, leading /, .., odd names
    |
    v
2. Ignore rules     on the path as written
    |
    v
3. Resolve symlinks on the part of the path that exists
    |
    v
4. Containment      is the real location inside the real root?
    |
    v
5. Ignore rules     again, on the real location
```

Step 5 matters because a link inside the vault can point at `.git`. It also defeats
Windows short names such as `SECRET~1`, which resolve to their long form in step 3.

## Same rules on every platform

Text checks do not depend on the operating system. `C:/x`, `a.md:stream` and
`\\server\share` are refused on Linux too. A vault synced between machines then
behaves the same everywhere, and all cases are testable on one machine.

The cost is small. Obsidian forbids `:` and `\` in note names anyway. On Linux, a
file with such a name is simply invisible to vaultd.

We also refuse Windows device names (`NUL`, `con.md`) and names ending in a dot or
space, because Windows would silently open something else: `secret.` means `secret`.

## Missing files

A new note's path does not exist yet, so `filepath.EvalSymlinks` cannot resolve all
of it. `evalExisting` walks up to the deepest part that exists, resolves that, and
adds the rest back.

A dangling symlink needs special care: `Stat` says "does not exist", but writing
through it would create a file at the link's target, anywhere on disk. So we use
`Lstat`, which sees the link itself, and refuse if it cannot be resolved.

## Known limit

There is a short gap between checking a path and opening it. A program already on
your machine could swap a folder for a symlink in that gap. vaultd does not defend
against this: such a program can already read your vault directly.

## Listings

Walks do not follow symlinks, so a link loop cannot trap them. A symlinked note can
still be opened by its path if it passes the jail, but it is not listed or indexed.
