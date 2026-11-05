# Design: the audit log

One JSON line per tool call, so you can answer "what did that app do?" with `grep`.

```json
{"ts":"2026-01-02T03:04:05Z","app":"cursor","tool":"vault.read","args":{"path":"a.md"},"args_bytes":14,"ok":true,"duration_ms":1,"result_bytes":120}
```

## Where it sits

The audit layer wraps the tool router, so refused calls are recorded too: an attempt to
call a locked tool is exactly what you would want to find later.

It sees only the tool name and arguments, not HTTP headers. It therefore cannot log a
token, by construction.

## What is cut

Strings over 500 characters, at any depth, including map keys and error messages. The log
records what happened; it is not a second copy of your notes. Set `log_arguments = false`
to keep only names and sizes.

`args_bytes` goes beyond the spec. With arguments off, it keeps the "sizes" the setting
promises.

The file is created readable only by you, since arguments can hold private text.

## Rotation

`max_files` counts all files. With 3: `audit.jsonl`, `.1`, `.2`. A line is never split
across files, and each file stays under `max_size_mb`.
