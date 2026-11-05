# Performance: search index

## Goals

- Index 20,000 notes in under 60 seconds.
- An edit appears in search within 2 seconds.

## Method

`TestIndexes20000NotesWithinOneMinute` generates 20,000 notes of about 2.3 KB in 20
folders, each with frontmatter, a heading and a wikilink, then runs a full sync.
`TestWatcherSeesChanges` fails if any kind of edit takes more than 2 seconds to appear.

Both are in `internal/index/index_test.go` and run with `go test`.

## Results

Measured on an Intel Core Ultra 5 125H (18 threads), under WSL2, on Go 1.27.3:

| Step                                | Time     | Goal    |
|-------------------------------------|----------|---------|
| Full index of 20,000 notes          | 3.9-4.0 s | < 60 s  |
| Sync when nothing changed           | 67-70 ms  |         |
| One search over 20,000 notes        | < 1 ms    |         |

