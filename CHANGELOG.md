# Changelog

## v0.1.2 — 2026-11-24

- aacraid: a paged read whose close is acknowledged with status `0xc5` is now rejected, as the vendor tool does (it only accepts `CT_OK`), and retried once in the same cycle; a second rejection is an error. v0.1.1 published the data of such a read. `hwraid_transfer_close_unconfirmed_total` counts rejected reads, including those of a collection returned after its timeout (counted in the next cycle). Once the cycle times out or the exporter stops, the read is not retried.
- aacraid: controller commands are sent from a dedicated thread that blocks asynchronous signals. The `aacraid` driver loses one management command block, until the next reboot, each time a signal interrupts a command waiting for the card; the Go runtime's own preemption signal (`SIGURG`) could do so. Once the driver's pool is empty, every management tool fails with `ENOMEM`.
- On `SIGTERM`, the exporter starts no new cycle and the cycle in progress sends no further controller command: an open paged transfer only completes its shortest sequence (first chunk, then close) and is not published, and only the command already sent is awaited, up to 20 s. If it does not return in time, the exporter exits with code 1. A cycle interrupted by the stop is not published. Collection timeout and publication share one deadline. The systemd unit sets `TimeoutStopSec=30s`; the Compose examples set `stop_grace_period: 30s`.
- Every failed collector of a cycle is logged, not only the first one. `ENOMEM` on a controller command is reported with its two possible causes: an exhausted driver pool, which only a reboot restores, or a DMA buffer the driver could not allocate for a large command, which is transient.
- aacraid: volume write cache published: `hwraid_volume_write_cache_policy` (configured setting: `write_back_always`, `write_back_when_protected`, `write_through`) and `hwraid_volume_write_cache_enabled` (current state), read from the volume information already collected, with no extra controller command.
- Alert rules: `HwraidVolumeWriteCacheUnprotected` (critical: write cache active while cache protection is missing, failed or charging), `HwraidVolumeWriteCacheProtectionUnknown`, `HwraidVolumeWriteCacheUnknown` (write cache setting or state not recognised), `HwraidVolumeWriteCacheAlwaysOn`, `HwraidVolumeWriteCacheInactive`; during a failed read their series keep, for 10 minutes, the value they had in the last successful read; `HwraidCacheProtectionFailed`, `HwraidCollectionStale` and `HwraidCollectionNeverSucceeded` now say what to do.
- Documentation of the `aacraid` driver defect (`docs/aacraid-defaut-pilote.md`) and `tools/aacraid-fib-trace.sh`, a kernel probe that names the programs losing command blocks; it writes to a new private directory, names its probe per run, checks that the probe is removed and reports the returns the probe missed.
- The systemd unit is attached to each GitHub release and listed in `SHA256SUMS`.

## v0.1.1 — 2026-11-23

- Apache License 2.0 (`LICENSE`, `NOTICE`, SPDX headers).
- Third-party licences shipped with the binaries: `/licenses` in the container image, licence archive next to the release binary.
- Security policy (`SECURITY.md`).
- aacraid: a paged-transfer close acknowledged with mark `0xc5` (seen when another tool such as maxView queries the card at the same time) no longer fails the collection; it is counted in `hwraid_transfer_close_unconfirmed_total`. v0.1.0 failed the volume and disk collectors on such a cycle.
- CI: `actions/checkout` 7.0.1 and `actions/setup-go` 7.1.0, pinned by commit.
- Docker seccomp profile (`deploy/docker/seccomp.json`): Docker's default profile without `iopl`/`ioperm`, which `SYS_RAWIO` otherwise allows.
- Collection stops sending controller commands once the cycle deadline has passed (a paged read in progress is still closed).
- Capabilities are kept only for modules with a card whose nodes were prepared.
- Discovery: unreadable or empty mandatory PCI attributes and an unreadable `driver` link are host errors instead of empty values.

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
