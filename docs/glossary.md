# Glossary

**Allowlist.** A list of the only things permitted. Anything not on it is refused.

**Atomic write.** A write that either fully happens or does not happen at all.
A crash never leaves half a file.

**Backlink.** A note that links to the note you are looking at.

**Bearer token.** A secret string an app sends with each request. Whoever holds
("bears") it is treated as that app.

**bm25.** The standard formula for ranking search results by relevance.

**Debounce.** Waiting for a burst of events to go quiet, then acting once.

**DNS rebinding.** An attack where a malicious website tricks your browser into
sending requests to a service on your own machine.

**Frontmatter.** A block of YAML settings at the very top of a Markdown note,
between two `---` lines.

**FTS5.** The full-text search engine built into SQLite.

**Gateway.** A program that sits between apps and the things they use, and
applies rules in one place.

**Hot reload.** Applying a changed config file without restarting the program.

**Loopback address.** An address such as `127.0.0.1` that only programs on the
same machine can reach.

**MCP (Model Context Protocol).** The standard way AI apps talk to tool servers.

**Path jail.** The check that keeps every file access inside the vault folder.

**stdio.** Talking to a program through its standard input and output streams.

**Streamable HTTP.** The way MCP runs over HTTP.

**Symlink (symbolic link).** A file that points to another file or folder.

**TOML.** A simple text format for configuration files.

**Upstream.** Another MCP server that vaultd forwards tool calls to.

**Vault.** An Obsidian vault: a folder of Markdown files.

**Wikilink.** Obsidian's link syntax: `[[Note name]]`.
