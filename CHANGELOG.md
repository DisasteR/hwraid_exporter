# Changelog

## v0.1.2 — 2026-11-24

Changes since v0.1.1. The `aacraid` driver loses one management command block, until the next reboot, each time a signal ends the wait of a program for the card before its answer arrives (`-ERESTARTSYS`; cards answered through `aac_intr_normal`, series 6 included, kernel 4.6 and later); once its pool of 8 is empty, no management tool can reach the card and the shutdown cache flush command cannot be sent. Most of this release protects against it: see `docs/aacraid-defaut-pilote.md`.

### Card commands and stop

- aacraid: controller commands are sent from a dedicated thread that blocks asynchronous signals (v0.1.1 could be interrupted by the Go runtime's own `SIGURG`). Only the fault signals stay unblocked; if the mask cannot be set, no command is sent.
- On `SIGTERM`, `SIGINT` or `SIGHUP`, the exporter starts no new cycle and no new collection, and sends no new command, `GET_HBA_INFO` included (the driver serves it under the same lock as management commands). The stop and the cycle deadline (compared with the clock) are checked on the ioctl thread right before each command; only the first chunk and the close of an open paged transfer are exempt, and such a read is not published. The command already sent is awaited with no limit of its own and a warning every 10 s: the supervisor's stop timeout is the only limit (`TimeoutStopSec=30s` in the systemd unit, `stop_grace_period: 30s` in the Compose examples). That bound is an availability trade-off: a command still running after 30 s is killed, losing a block and, for a command larger than a block, risking its DMA buffer (docs/aacraid-defaut-pilote.md).
- From the start of a stop, `/metrics` serves no hardware value (`hwraid_snapshot_ready 0`); a cycle interrupted by the stop is not published, and errors of collections still running are logged. `/metrics` also stops serving a view older than three intervals plus the collection timeout (stuck collection loop).
- `SIGPIPE` is ignored while serving: a log reader that goes away no longer kills the exporter during a card command.

### aacraid protocol

- A paged read whose close is acknowledged with `0xc5` is rejected, as the vendor tool does (it uses the data only for `CT_OK`), and retried once in the same cycle if its chunks were valid and the cycle is neither expired nor stopped; a second rejection is an error. v0.1.1 published the data of such a read.
- Every paged response must announce the size the card always uses (496 bytes, from 1 057 captured responses), and a transfer is engaged only if the open response carries `CT_OK` (`0xda`): a truncated or non-conforming open is followed by no chunk and no close, and a truncated close invalidates the read.
- A ZMM state word with status bits but without the installed bit no longer publishes `hwraid_cache_protection_present 1`: presence is omitted and the state published as `unknown`.
- When the capabilities read (706) fails, a card whose model and firmware are not qualified still publishes its unsupported collectors, and a qualified card publishes the reads it prevented as failed.

### Volume write cache

- New metrics `hwraid_volume_write_cache_policy` (configured setting: `write_back_always`, `write_back_when_protected`, `write_through`) and `hwraid_volume_write_cache_enabled` (current state), read from the volume information already collected, with no extra controller command.

### Per-card tracking

- The history kept per card (last success dates, `0xc5` counter, memory-served success, unsupported collectors) and the 706 and 708 answers kept in memory are tied to the card's hardware identity, one definition shared by the exporter and the module: serial number, PCI IDs, model, firmware (plus the address and device node). Any field read differently shows another card or firmware: the history is forgotten and the answers are read again. An unreadable field alone proves neither continuity nor replacement: nothing is forgotten, but nothing is kept in memory either (706 read every cycle), and memory-served successes and unsupported collectors are reused only after an effective collection under a complete identity.
- During a collection timeout or a failed node check, a collector served from memory (`capabilities`) stays successful if its last read succeeded on the same card, and collectors known as unsupported stay unsupported. While the node check fails, every other supported collector is published as failed (its prerequisite failed), with its last success date, and collectors known as unsupported stay published as such, so that losing all readings is reported even if those cycles alternate with timeouts, before or after a first success.
- `hwraid_transfer_close_unconfirmed_total` counts rejected reads, those of a collection finished after its timeout included: counted for the card of the cycle if returned within it, otherwise at the next collection of the same PCI address, and dropped if another card is observed there at that collection. Known limit: if the other card was observed at an earlier cycle, they are counted for it (a card swap requires a power cycle in practice, which restarts the exporter).

### Alert rules

- Rules use the published state only, never the last known value after a failed read; state, write cache, disappearance and intermittence alerts stay active for 10 minutes from the first rule evaluation without their condition (`keep_firing_for`, Prometheus 2.42 or later).
- Write cache: `HwraidVolumeWriteCacheUnprotected` (critical: cache active while protection is missing, failed or charging; to be checked in ARCCONF for every affected volume after changing the setting), `HwraidVolumeWriteCacheProtectionUnknown`, `HwraidVolumeWriteCacheUnknown`, `HwraidVolumeWriteCacheAlwaysOn`, `HwraidVolumeWriteCacheInactive`; every volume is evaluated, members of an array included.
- `HwraidVolumesNotMonitored` (warning, 1 h): a card recognised by a module whose volumes are not read (model and firmware not qualified, or state reads not announced by the capabilities answer).
- `HwraidCollectionIntermittent`: at least six success/failure transitions of a currently published collector in an hour, or four in four hours, which would otherwise keep `for` delays (up to 2 hours) from ever completing.
- `HwraidVolumeDisappeared` and `HwraidControllerDisappeared` raise one alert per object, even after a rename or a firmware update, and keep firing through a failed read. `HwraidCollectionStale` ignores a collector that has become unsupported. `HwraidCacheProtectionFailed`, `HwraidCollectionStale` and `HwraidCollectionNeverSucceeded` say what to do.

### Diagnostics and tools

- Every failed read of a published collection is logged, with the reads it prevented, errors of a collection finished after its timeout included. `ENOMEM` on a controller command is reported with its two possible causes: an exhausted driver pool, or a DMA buffer the driver could not allocate for a large command.
- `tools/aacraid-fib-trace.sh`, a kernel probe that names the programs losing command blocks. It writes to a new private directory (100 MiB by default), names its probe per run, drains the buffer before removing the probe and checks the removal, including on `SIGHUP`, `SIGINT`, `SIGTERM` or a closed standard output. Its summary says « trace complète » only if the capture started, nothing was missed or lost, the reader did not fail and the removal succeeded.

### Deployment and release

- The systemd unit is attached to each GitHub release and listed in `SHA256SUMS`; the documented check compares exact file names.
- Documentation: operating rules for the driver defect (stop with a 30 s grace period, never `docker kill` or `docker rm -f`, do not freeze the container, write cache with a failed protection module, exhausted pool, card replacement), per-card acceptance check.
- `scripts/check-public.sh` rejects email addresses outside `lore.kernel.org` links.

### Upgrade from v0.1.1

- The first stop is performed by the v0.1.1 process, which does not protect its commands: stop it once, with the 30 s grace period, and start v0.1.2 directly (docs/deploiement.md).

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
