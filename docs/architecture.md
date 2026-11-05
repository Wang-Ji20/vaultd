# Architecture

vaultd is one program. Apps talk to it, and it talks to the vault.

```
AI app --HTTP--> Host check --> token check --+
                                              v
AI app --stdio--------------------------> MCP server --> tool functions --> path jail --> .md files
                                          (internal/server)   |             (internal/vault)    |
                                                              v                                 | changes
                                                         search index <---- file watcher <------+
                                                         (internal/index)
```

| Package           | Job                                                  |
|-------------------|------------------------------------------------------|
| `cmd/vaultd`      | The command line                                     |
| `internal/config` | Load, validate, save and hot reload `config.toml`    |
| `internal/server` | Expose tools over HTTP and stdio; check tokens       |
| `internal/vault`  | Path jail, note parsing, reading and safe writing    |
| `internal/index`  | Full-text search index and file watcher              |
| `internal/upstream` | Connections to other MCP servers, allow/lock rules |
| `internal/audit`  | JSON Lines record of tool calls, rotated by size     |

A tool function does little itself. It checks limits, calls the vault package and
encodes the result as JSON. All safety rules for paths live in the vault package,
so no tool can forget them.

A tool call passes through these layers on the MCP server, outermost first:

```
count running calls --> audit --> route upstreams --> tool
```

Upstream tools go to `internal/upstream`, which holds the allow and lock rules and the
connections to other MCP servers:

```
tool router --> allowed? --> upstream connection --> other MCP server
```

## Design documents

- [Configuration](design/config.md)
- [Note parsing](design/parsing.md)
- [The path jail](design/path-jail.md)
- [The search index](design/search-index.md)
- [The HTTP endpoint](design/endpoint.md)
- [Safe writes](design/writes.md)
- [The upstream proxy](design/upstream.md)
- [The audit log](design/audit.md)

## Performance

- [Search index](perf/search-index.md)
