# Design: safe writes

A model may be following instructions hidden in a note, so writes are narrow: add to a
note, or create a new one, and only in folders you chose.

## What is not possible

There is no delete and no overwrite tool. This is the main safety property. The worst a
confused model can do is add unwanted text, and that is easy to see and undo.

## Checks

A write first passes the [path jail](path-jail.md), then the write policy:

```
AI/log.md  -->  path in a writable folder?  -->  real location in a writable folder?
```

Both must hold. Checking only the path would let a symlink in `AI/` write to
`Private/`. Checking only the real location would let a symlink in `Private/` write to
`AI/`, which is odd enough to refuse.

## Atomic replace

```
1. write .vaultd-1234.tmp in the same folder
2. fsync, so the bytes are really on disk
3. rename over the note
```

A reader, including Obsidian, sees the whole old note or the whole new one. A crash
leaves at most a stray `.tmp` file. It must be the same folder, because rename is only
atomic within one file system.

## Create never overwrites

Renaming would replace a file another program just created. So `vault.create` publishes
the temp file with a hard link instead, which fails if the name is taken. File systems
without hard links use `O_EXCL`, which is still exclusive but not atomic.

## Known limit

An append reads the note, then replaces it. If Obsidian saves in that moment (well under
a millisecond of work), its save is lost. vaultd has a lock against its own concurrent
writes, but it cannot lock out other programs. The alternative, appending in place, is
not atomic: a crash could leave half a block.

## Line endings

An append follows what most of the note uses, LF or CRLF, so the file stays consistent.

## Memory lines

`memory.remember` turns line breaks in the text into spaces. Each memory is then exactly
one line. `memory.recall` depends on this, and it stops one memory from faking another
by including a line break and a date.

## Git autocommit

| Choice                      | Why                                                 |
|-----------------------------|-----------------------------------------------------|
| `git commit -- <file>`      | The spec's plain `git commit` would also sweep in whatever the user had staged |
| `commit.gpgsign=false`      | A passphrase prompt would hang the tool call        |
| `GIT_TERMINAL_PROMPT=0`     | The same, for password prompts                      |
| 30-second timeout           | A stuck hook must not hold the write lock forever   |
| No shell, `--literal-pathspecs` | A note named `*.md` or `$(x).md` means only itself |

A failed commit is logged, not returned. The note is already written, and telling the
model it failed would invite it to write again.
