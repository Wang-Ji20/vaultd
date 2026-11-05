# Design: the HTTP endpoint

One address serves all apps: `http://127.0.0.1:8765/mcp`.

## Layers of a request

```
request --> draining? --> Host check --> route --> token check --> MCP handler
              503           403                       401
```

## Why each check exists

**Loopback only.** The address is checked when the config loads. Once the system has
bound it, the actual address is checked again, since a name like `localhost` is only
known after it is resolved.

**Host check.** Binding to loopback is not enough. In DNS rebinding, a malicious website
makes its own domain point to `127.0.0.1`, and your browser then sends requests to
vaultd. But the browser still sends `Host: evil.example`, so vaultd refuses anything
except its exact configured `host:port`.

**Tokens.** Each app has its own. Only the SHA-256 hash is stored, so reading the config
file does not reveal a token. `FindApp` compares against every app in constant time, so
timing reveals nothing about a guess.

Tokens are checked against the current config on each request, so a new or revoked token
works at once. The SDK ties each session to the app that opened it: app B cannot use
app A's session ID, even with a valid token of its own.

SHA-256 is fine here, unlike for passwords. A token is 32 random bytes, and nobody can
guess that no matter how fast the hash is.

## Graceful shutdown

The obvious `http.Server.Shutdown` waits for every open connection. MCP clients keep an
event stream open for as long as they are connected, so it would always use up its whole
time limit. vaultd stops in stages instead:

```
1. refuse new requests (503)
2. wait for running tool calls, 10 s at most
3. close MCP sessions, which ends the event streams
4. shut the HTTP server down
```

## What a config change restarts

| Setting changed       | Effect                                          |
|-----------------------|-------------------------------------------------|
| `apps`                | Next request                                    |
| `vault.writable_folders`, `git_autocommit` | Next write                |
| `vault.path`, `ignore`, `memory_file` | Index is cleared and rebuilt, watcher restarted |
| `server.listen`       | Logged; needs a restart                         |

The index is cleared rather than synced, so a folder that was just added to `ignore`
is not searchable, even briefly.
