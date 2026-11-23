# Security policy

hwraid_exporter starts as root to create its management nodes, then drops its privileges and sends commands to RAID controller firmware. A flaw in it can affect the host or the controller: please report it privately.

## Reporting a vulnerability

Use GitHub's private reporting: **Security → Report a vulnerability** on this repository. Do not open a public issue for a security problem.

Please include the version (`hwraid-exporter version`), the deployment mode (Docker or systemd), the controller model and firmware, and the steps to reproduce. Do not attach raw captures of controller exchanges: they contain serial numbers and other identifiers of your hardware (see the [capture guide](docs/guide-capture.md)).

You will get an acknowledgement within a week. Fixes are released as a new patch version; the advisory is published once a fixed version is available.

## Supported versions

Only the latest release receives security fixes.

## Scope

In scope, among others:

- any way to make the exporter send a controller command outside its allowlist, or a command to the wrong controller;
- privileges kept after the drop (identity, capabilities, `no_new_privs`), or files it creates outside its node directory;
- anything reachable over HTTP beyond reading `/metrics`;
- secrets or host identifiers leaking into logs, metrics or the repository.

Out of scope: the HTTP listener is plain HTTP without authentication by design, listening on `127.0.0.1` by default; exposing it is a deployment choice ([deployment guide](docs/deploiement.md)).
