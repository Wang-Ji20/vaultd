# vaultd

A small local gateway that gives every AI app on your machine the same access to your
Obsidian vault and to other tool servers, through one address:

```
http://127.0.0.1:8765/mcp
```

It is one program with no runtime dependencies. It works with Obsidian closed, because it
reads the Markdown files directly. It only listens on your own machine.

```
Claude Desktop ─┐
Cursor ─────────┤                   ┌─> your vault (search, read, safe writes)
Claude Code ────┼─> vaultd ─────────┤
DeepSeek Harness┘   tokens, rules,  └─> other MCP servers (only the tools you allow)
                    audit log
Browser ─────────>  admin portal
```

AI apps can search and read notes, and add to notes in folders you choose. They cannot
delete or overwrite anything: there are no such tools.

## Setup

Download a release, or build it. Building needs Go 1.27 or newer:

```
CGO_ENABLED=0 go build ./cmd/vaultd
```

If your system's Go is older, let Go fetch the right version for this project:

```
GOTOOLCHAIN=go1.27.3 CGO_ENABLED=0 go build ./cmd/vaultd
```

Then answer a few questions, set a password for the portal, and start it:

```
vaultd init
vaultd hash-password --write
vaultd run
```

`init` prints a token for your first app. Save it: it is shown only once. Open the portal at
<http://127.0.0.1:8765/admin/> to manage everything else.

To keep vaultd running in the background:

```
vaultd install
vaultd start
```

## Connect your apps

Each app gets its own token. Create one with `vaultd token new <name>` or in the portal,
which also shows these snippets ready to copy. Replace `YOUR_TOKEN`.

**Claude Desktop**, in `claude_desktop_config.json`. This starts vaultd over stdio, so it
needs no token:

```json
{
  "mcpServers": {
    "vaultd": { "command": "/path/to/vaultd", "args": ["run", "--stdio"] }
  }
}
```

**Cursor**, in `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "vaultd": {
      "url": "http://127.0.0.1:8765/mcp",
      "headers": { "Authorization": "Bearer YOUR_TOKEN" }
    }
  }
}
```

**Claude Code**:

```
claude mcp add --transport http vaultd http://127.0.0.1:8765/mcp --header "Authorization: Bearer YOUR_TOKEN"
```

**DeepSeek Harness**, in `cordis.patch.yml`:

```yaml
mcp:
  vaultd:
    client: "@deepseek-ai/dsh-mcp-client"
    transport: streamable-http
    url: http://127.0.0.1:8765/mcp
    headers:
      Authorization: Bearer YOUR_TOKEN
```

The DeepSeek Harness snippet follows the fields in the spec, and has not been tried against
that app. Check the entry's shape against its documentation.

## Learn more

- [User manual](docs/user-manual.md): all commands, settings and tools
- [Architecture](docs/architecture.md): how the parts fit together
- [Documentation index](docs/README.md)
- [SPEC.md](SPEC.md): what was specified

## Develop

```
go vet ./...
go test ./...
scripts/release.sh 1.0.0    # builds linux/amd64, linux/arm64 and windows/amd64 into dist/
```
