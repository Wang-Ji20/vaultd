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
