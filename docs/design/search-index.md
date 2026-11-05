# Design: the search index

vaultd keeps a SQLite database, `index.db`, with a copy of each note's words. It
holds nothing that cannot be rebuilt from the vault, so an index from an older
version is replaced rather than migrated.

## Staying current

```
start --> watch all folders --> sync in the background
                 |
                 v
        file event --> wait 500 ms for quiet --> look at the file --> update index
```

Watching starts before the first sync, so no change can fall between them.

**Sync** compares each file's time and size with the index. If they differ, it reads
the file and compares a hash. A file that was only touched, for example by a fresh
git checkout, just gets its new time stored.

**The watcher never trusts the event type.** After the wait it looks at what the path
is *now*. This one rule covers all the ways editors save:

| Editor saves by          | Events                 | Path is now | Action    |
|--------------------------|------------------------|-------------|-----------|
| Writing in place         | write                  | a file      | reindex   |
| Renaming a temp file over| create (or rename)     | a file      | reindex   |
| Delete, then create      | remove, create         | a file      | reindex   |
| Deleting                 | remove                 | missing     | remove    |
| A folder moved in        | create                 | a folder    | full sync |

If the system drops events (an "overflow"), nobody knows what changed, so vaultd runs
a full sync.

## Search during indexing

Notes are stored in batches of 500 transactions at a time. SQLite's write-ahead log
lets searches read at the same time, and each finished batch becomes visible. So
search returns partial results, marked `"indexing": true`.

## Queries are never syntax

FTS5 has its own query language (`OR`, `*`, `title:x`). A user or model must not be
able to use it by accident, or cause query errors. Every word is therefore quoted:

```
say "hi" OR x   -->   "say" """hi""" "OR" "x"
```

All words must match. `OR` is just a word.

## Ranking: a correction to the spec

The spec gives `bm25(notes_fts, 10.0, 5.0, 1.0)` for title, tags and body. But bm25
takes one weight per column in table order, and the first column is `path`:

```
notes_fts columns:  path   title   tags   body
spec's weights:     10.0   5.0     1.0    (1.0 by default)
vaultd's weights:   0.0    10.0    5.0    1.0
```

vaultd uses the second line, which gives the weights the spec intended. A test checks
that a title match beats a tag match, which beats a body match.

## Backlinks are resolved when asked

The `links` table stores each link as written, normalized (`[[Note#h|x]]` becomes
`note`). Which note it means is decided at query time.

The other way would store the resolved note. But then creating a new `Note.md` with
a shorter path would change what old links mean, and every linking note would need
reindexing.

## Memory lines

`memory.recall` must return single memories, but `notes_fts` stores whole files. So the
memory file also gets a table of its own, `memory_fts`, with one row per line. The spec's
schema does not list this table. It is the smallest way to get a full-text search that is
"restricted to memory file lines". The table is replaced whenever the file changes.

## Notes over 1 MB

These are listed by name but their text is not read, the same limit as `vault.read`.

## Alternatives

- **No index, scan files per search.** Milestone 1 did this. It takes seconds on a
  large vault and has no ranking.
- **A separate rowid map.** The search table shares rowids with `notes`, so deleting
  a note's text is instant. A lookup by `path` would scan the whole search table.
