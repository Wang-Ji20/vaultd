# Design: running as a service

`vaultd install` registers vaultd with the operating system, using the
`kardianos/service` library.

| System  | Kind                | Runs as        | Logs                     |
|---------|---------------------|----------------|--------------------------|
| Linux   | systemd user service | You           | `journalctl --user -u vaultd` |
| Windows | Windows Service     | LocalSystem    | `vaultd.log` in the data folder |

## Absolute paths

`install` writes the absolute config and data paths into the service definition. A Windows
Service runs under another account, so its `%APPDATA%` is not yours. Without this, it
would look for a config in the wrong place.

`install` also loads the config first. It is better to fail then than to leave a service
that cannot start.

## Environment

A service does not get your shell's environment, so `env:NAME` references need the variable
set where the service can see it. See the [user manual](../user-manual.md#run-as-a-service).

## Shutdown

Stop runs the same graceful path as Ctrl+C: refuse new requests, let running calls finish
(10 s at most), stop upstream children, close the audit log and the database.

If vaultd is killed outright, nothing runs. See
[child processes](upstream.md#child-processes) for how its children still end.

## What has been tested

- **Linux:** the whole cycle by hand: install, start, health check, stop, uninstall.
- **Windows:** the service-related code compiles, and `vaultd run` plus all tests work on
  Windows. Installing a Windows Service needs administrator rights, and has **not** been tried.
