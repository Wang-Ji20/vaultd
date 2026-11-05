# Design: configuration

All settings live in one TOML file, `config.toml`. See the
[user manual](../user-manual.md) for its location.

## Strict by default

Loading fails on the first problem. The reasons behind the less obvious rules:

- **Unknown keys are errors.** A typo such as `lockd = [...]` would otherwise
  silently remove a safety rule.
- **`env` and `headers` values must all be `env:NAME` references.** vaultd cannot
  tell whether a value is secret, so it accepts no literals at all. Error messages
  name the key and never the value.
- **Upstream names may not contain dots or be `vault` or `memory`.** Tool names are
  `<upstream>.<tool>`, so these names could impersonate vaultd's own tools.
- **`localhost` is accepted as a listen host** besides loopback IP addresses, since
  it can only mean this machine.

## Validation has no side effects

`Validate` changes nothing on disk, so rejecting a config is always safe. Creating
missing writable folders is a separate later step, `EnsureWritableFolders`.
That step also catches a writable folder that is a symlink leading out of the vault,
which plain text checks cannot see.

## Hot reload

The `Store` holds the current config behind an atomic pointer. A request reads one
snapshot and is not affected by a reload halfway through.

Every change takes the same path. Nothing is touched until the new config is valid:

```
copy --> change --> validate --> back up old file --> write new file --> swap
```

The new file is written to a temporary name and renamed into place, so nobody ever
reads half a config. vaultd therefore watches the *folder*, not the file: a rename
replaces the file, and a watch on the old file would never fire again.

An edit by hand takes a shorter path, since the file already changed: load it, validate
it, swap if valid. If invalid, vaultd logs an error and keeps the last good config.

Saving rewrites the file, so comments in it are lost. The backup keeps the old version.

## Backups

Each save first copies the current file to `config.<timestamp>.toml` in the data folder.
The last 10 are kept. A restore counts as a save, so a restore can itself be undone.
