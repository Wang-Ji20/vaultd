# Design: the upstream proxy

The gateway forwards tool calls to other MCP servers. No server is special: IBKR and
Gmail are just config entries, and no Go code knows about them.

## One rule, one place

`upstream.Allowed` decides whether a tool may be used: it must be in `allow` and not in
`locked`. Config validation refuses a tool in both, but `Allowed` still lets `locked` win.
So a bug elsewhere cannot expose a locked tool.

A refused call never reaches the upstream. Hidden, locked and non-existent tools all get
the same answer, `tool not available`, which reveals nothing about what exists.

## Lazy connections

Nothing connects at startup. The first tool list or call does. So a vault-only session
never starts child processes, and a slow upstream does not delay startup.

A tool list waits at most 10 seconds. Tools that show up later are announced to apps
with a "tool list changed" notification.

## Failure and restart

```
 connected --crash--> error --wait--> connecting --ok--> connected
                        ^                 |
                        +-----failed------+
              wait: 1 s, 2 s, 4 s ... 60 s
```

- **Restart needs no caller.** A timer runs the next attempt.
- **Callers don't wait for a dead upstream.** During the wait they get the last error at
  once. So a dead upstream costs nothing.
- **Only a connection that lasted 60 seconds resets the wait.** Otherwise a child that
  crashes right after starting would restart every second forever.
- **A connect attempt has its own timeout,** not the caller's. If the app that triggered
  it hangs up, the connection is still useful to the next one.

## Child processes

Closing a session closes the child's input, waits, then sends a terminate signal, then
kills it.

If vaultd itself crashes, nothing gets to do that. On Linux the child sees its input
close, which is how an MCP server knows to exit. On Windows, vaultd joins a *job object*
with "kill on close" at startup. Children inherit the job from their first instant, and
Windows closes the job when vaultd dies for any reason. Joining first means no child can
slip out between starting and being added.

## Publishing tools

Upstream tools are added to the SDK's MCP server as they are discovered, and removed when
no longer allowed. The SDK then notifies apps. The alternative, merging lists by hand in
a middleware, is simpler but gives no such notification, so an app would never learn of
an upstream that came up late.

The SDK refuses a malformed tool by panicking. vaultd catches that and skips the tool, so
one bad upstream cannot bring the gateway down.

## Descriptions and the data warning

The spec passes upstream descriptions through unchanged, and also wants tool descriptions
to warn that returned content is data. vaultd keeps upstream descriptions exactly as they
are and puts the warning in the server's instructions, sent to apps on connecting.

## Secrets

`env:NAME` values are read at connect time. Errors name the variable, never its value.
