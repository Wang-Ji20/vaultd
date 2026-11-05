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
vaultd run --stdio     # serve one app over standard input and output
vaultd version
```

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

`vault.read` returns the frontmatter separately, so `content` does not repeat it.

## Test

```
go vet ./...
go test ./...
```
