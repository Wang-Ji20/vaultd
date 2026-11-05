# Architecture

vaultd is one program. Apps talk to it, and it talks to the vault.

```
AI app --stdio--> MCP server --> tool functions --> path jail --> .md files
                  (internal/server)                 (internal/vault)
```

| Package           | Job                                                  |
|-------------------|------------------------------------------------------|
| `cmd/vaultd`      | The command line                                     |
| `internal/config` | Load and validate `config.toml`                      |
| `internal/server` | Expose tools over MCP                                |
| `internal/vault`  | Path jail, note parsing, reading files               |

A tool function does little itself. It checks limits, calls the vault package and
encodes the result as JSON. All safety rules for paths live in the vault package,
so no tool can forget them.

## Design documents

- [Configuration](design/config.md)
- [Note parsing](design/parsing.md)
- [The path jail](design/path-jail.md)
