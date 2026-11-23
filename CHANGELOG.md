# Changelog

## Unreleased

- Apache License 2.0 (`LICENSE`, `NOTICE`, SPDX headers).
- Third-party licences shipped with the binaries: `/licenses` in the container image, licence archive next to the release binary.
- Security policy (`SECURITY.md`).

## v0.1.0 — 2026-11-23

First release. `aacraid` module, qualified on Adaptec 6805, firmware 5.3-0[19204].

- Volume (mirrors, assemblies) and member disk states, normalized RAID level.
- Disk inventory (vendor, model, firmware, serial number, WWN, capacity), location (connector, position, phy) and link to the Linux device (`sgX`).
- Cache protection module (ZMM) state.
- Collection tracking: per-collector success, last success time, interval, per-card timeout.
- Example alert rules, tested with `promtool`.
- Docker image (`ghcr.io/disaster/hwraid_exporter`), static binary with its SHA-256 checksum, and systemd unit.
- Build information metric (`hwraid_exporter_build_info`: version, source revision, Go version).

Validated on hardware: optimal volume and disk states, inventory, location, Linux device, failed cache protection. Degraded, rebuilding and failed volume states and the charging and optimal cache protection states are tested on constructed data only.
