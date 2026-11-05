# User manual

## Build

vaultd needs Go 1.27 or newer.

```
CGO_ENABLED=0 go build ./cmd/vaultd
```

If your system Go is older and refuses with `GOTOOLCHAIN=local`, let Go fetch the
right version just for this project:

```
GOTOOLCHAIN=go1.27.3 CGO_ENABLED=0 go build ./cmd/vaultd
```

## Configure

Copy `config.example.toml` to the default location and set `vault.path`:

| System  | Config file                                                   | Data folder                   |
|---------|---------------------------------------------------------------|-------------------------------|
| Linux   | `$XDG_CONFIG_HOME/vaultd/config.toml` or `~/.config/vaultd/…` | `~/.local/share/vaultd/`      |
| Windows | `%APPDATA%\vaultd\config.toml`                                | `%LOCALAPPDATA%\vaultd\`      |

Any command accepts `--config <path>` to use another file.

## Run

```
vaultd run             # serve all apps at http://127.0.0.1:8765/mcp
vaultd run --stdio     # serve one app over standard input and output
vaultd token new NAME  # create a token for an app and print it once
vaultd hash-password   # prompt for the admin password, print its hash
                       # (--write saves it in the config instead)
vaultd reindex         # rebuild the search index from scratch
vaultd version
```

The search index is kept in `index.db` in the data folder. It can be deleted at any
time; vaultd builds it again on the next start. While it is being built,
`vault.search` returns what is indexed so far and says `"indexing": true`.

On Linux, each vault folder uses one system "inotify" watch. If a very large vault
reaches the limit, vaultd logs a warning and changes there appear only after a
restart. Raise `fs.inotify.max_user_watches` to fix this.

Changes to `config.toml` apply without a restart, except `server.listen`. If a change
is invalid, vaultd logs an error and keeps using the last good config.

`GET /health` answers `{"status":"ok","version":"..."}` without a token.

## Connect an app over HTTP

Create a token, then give the app the address and the token:

```
vaultd token new cursor
```

```json
{
  "mcpServers": {
    "vaultd": {
      "url": "http://127.0.0.1:8765/mcp",
      "headers": { "Authorization": "Bearer vd_..." }
    }
  }
}
```

Use the address exactly as `server.listen` has it. vaultd refuses any other `Host`, so
`http://localhost:8765` is refused when `listen` says `127.0.0.1`. This blocks an attack
called [DNS rebinding](glossary.md).

`token new` refuses a name that already has a token. To replace a token, remove the
app's entry from `config.toml` and create it again.

## Connect an app over stdio

For Claude Desktop, add this to `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "vaultd": { "command": "/path/to/vaultd", "args": ["run", "--stdio"] }
  }
}
```

## Tools

| Tool           | What it does                                       |
|----------------|----------------------------------------------------|
| `vault.search` | Search notes by plain text                         |
| `vault.read`   | Read one note (up to 1 MB)                         |
| `vault.list`   | List the folders and notes directly inside a folder |
| `vault.recent` | List recently modified notes, newest first         |
| `vault.backlinks` | List the notes linking to a note                |
| `vault.append` | Add text to a note, creating it if missing         |
| `vault.create` | Create a new note; fails if the path is taken      |
| `memory.remember` | Add one dated line to the memory file           |
| `memory.recall` | Search the memory file, line by line              |

`vault.read` returns the frontmatter separately, so `content` does not repeat it.

Writes only work inside `vault.writable_folders`. There is no tool to delete or
overwrite a note. The memory file must be inside a writable folder for
`memory.remember` to work.

### Git

If `vault.git_autocommit` is on and the vault is a git repository, each write becomes
one commit, such as `vaultd: vault.append AI/log.md (cursor)`. Only that file is
committed: anything else you have staged stays staged. If a commit fails, for example
because git has no `user.name`, the write still counts and vaultd logs a warning.

## Admin portal

Set a password, then open `http://127.0.0.1:8765/admin/`:

```
vaultd hash-password --write
```

| Page          | What you can do                                                    |
|---------------|--------------------------------------------------------------------|
| Overview      | See status and the last 20 calls; restore one of the last 10 configs |
| Apps & tokens | Create, rotate and revoke tokens; copy config for each client      |
| Vault         | Choose writable folders; switch git commits; rebuild the index     |
| Upstreams     | See connections; reconnect; allow or hide each tool                |
| Audit log     | Filter and search calls; click a row for its full JSON             |
| Settings      | Audit retention and arguments; session length; theme               |

Every change is validated and backed up first, and applies at once without a restart.

Saving from the portal rewrites `config.toml`, so comments in it are lost. Backups keep the
old versions in the data folder.

Tested in Chrome and Firefox on Linux, in light and dark mode. Windows has not been tested.

## Upstream servers

An upstream is any other MCP server. Add it to `config.toml`, and its allowed tools
appear to every app as `<upstream>.<tool>`:

```toml
[[upstreams]]
name = "notes2"
transport = "stdio"                      # or "http" with url = "..."
command = "path/to/server"
env = { API_KEY = "env:NOTES2_API_KEY" }  # read from vaultd's environment
allow = ["search"]
locked = ["delete_everything"]
```

- A tool not in `allow` is invisible and returns `tool not available`.
- A tool in `locked` can never be enabled. Listing it in both is a config error.
- Values in `env` and `headers` must be `env:NAME`. Set the secret in the environment
  vaultd runs in, so it never sits in the config file.
- vaultd connects when an app first needs the upstream. A crashed child process restarts
  by itself, waiting 1 s, then 2 s, up to 60 s between attempts.
- If an upstream is down, its tools report an error. Everything else keeps working.

## Audit log

Each tool call adds one line to `audit.jsonl` in the data folder, with the time, app,
tool, arguments, outcome, duration and result size. Strings over 500 characters are cut.
Set `audit.log_arguments = false` to record only names and sizes. Tokens, headers and
environment values are never logged.

When the file reaches `audit.max_size_mb` it is renamed `audit.jsonl.1`, and so on, up to
`audit.max_files` files in all.

## Test

```
go vet ./...
go test ./...          # includes the 20,000-note acceptance test
go test -short ./...   # skips it
```
