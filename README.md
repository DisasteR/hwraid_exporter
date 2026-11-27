# hwraid_exporter

Prometheus exporter for hardware RAID controllers. It talks directly to the Linux drivers and needs no vendor tool (ARCCONF, StorCLI, ssacli) at runtime.

> **Status: v0.4.0.** One driver module so far: `aacraid`. The only qualified model is the Adaptec 6805 (any firmware; validated with 5.3-0[19204]); other `aacraid` cards can be read with `-allow-unqualified` (all of them, or one card by serial number), at the operator's risk. What is validated on hardware and what is only tested is detailed [below](#what-it-monitors).

## Why another exporter

The goal is that an administrator can answer four questions from Prometheus: which array has lost its redundancy, which member is involved, is the rebuild progressing, and is the controller cache protected by a working module? v0.4.0 reports lost redundancy and the cache protection state. It names the member involved when the controller reports its disk failed or missing, and publishes the rebuilding state and its progress (progress reports not yet observed on hardware).

### Existing approaches

Surveyed in November 2026. The examples are representative, not exhaustive.

| Approach | Examples | What it needs | Limits |
|---|---|---|---|
| Run the vendor CLI and parse its output | Prometheus: `storcli.py` ([textfile collector scripts](https://github.com/prometheus-community/node-exporter-textfile-collector-scripts)), [hpraid_exporter](https://github.com/ProdriveTechnologies/hpraid_exporter) (ssacli, archived), [areca-prometheus-exporter](https://github.com/vilhelmprytz/areca-prometheus-exporter). [Netdata](https://github.com/netdata/netdata): `adaptecraid` (arcconf), `storcli`, `megacli`, `hpssa` (ssacli). Nagios/Icinga: [check_adaptec_raid](https://github.com/thomas-krenn/check_adaptec_raid). Status scripts: [hwraid](https://github.com/eLvErDe/hwraid) (`aacraid-status`…). | The vendor binary on the host, usually with root privileges (sudo or a privileged helper), depending on the tool. | Commands, metric names and model coverage vary with the tool and the vendor. The parsed output can change between tool versions. On `aacraid` series 6 cards (Linux 4.6 and later), a command interrupted by a signal, for instance a CLI killed on timeout, loses a driver command block until the next reboot ([driver defect](docs/aacraid-defaut-pilote.md)). |
| Ask the server's BMC over Redfish (out of band) | [idrac_exporter](https://github.com/mrlhansen/idrac_exporter) (iDRAC, iLO, XClarity), [sapcc/redfish-exporter](https://github.com/sapcc/redfish-exporter) | A BMC, BMC credentials. | Nothing to install on the host. The data is what the BMC exposes about storage: nothing without a BMC (NAS, white-box servers), and coverage varies with the vendor and BMC generation. |
| Read the disks | [smartctl_exporter](https://github.com/prometheus-community/smartctl_exporter), node_exporter `mdadm` collector | Access to the disks, or software RAID. | Complementary: SMART health of each disk does not give the state of a hardware array (degraded, rebuilding, failed). mdadm covers software RAID only. |
| Talk to the driver directly | [megactl](https://github.com/hmage/megactl) (older LSI MegaRAID, command-line tools in C) | Root. | Command-line tools, not exporters. Among the projects surveyed, we found no maintained Prometheus exporter of this kind. |

### What this exporter does differently

- **No vendor binary required on the host.** It sends the driver's own management commands, from a read-only allowlist. It can run next to the vendor tool; concurrent access may then make a read fail or be retried ([metrics](docs/metrics.md)).
- **Shared metrics contract.** Every module publishes the same metric names and state vocabularies (`optimal`, `degraded`, `failed`…), with the vendor's own wording kept in `vendor_*` labels, so dashboards and alerts do not depend on the vendor. What each module can fill in depends on the card.
- **Least privilege.** Root only for startup preparation (its own device nodes), then an unprivileged user that keeps only the capability the driver requires.
- **Collection decoupled from scrapes.** Cards are read on a configured schedule; `/metrics` serves the last published snapshot while it is fresh. On a collection timeout or a graceful shutdown, a command already sent to a card is allowed to finish; the service manager's stop timeout must leave room for it ([deployment](docs/deploiement.md)).
- **`aacraid`: the vendor tool's rules.** RAID levels, and volume and disk states, follow the rules of the vendor tool, established by analysis for interoperability ([legal basis](docs/base-legale-interoperabilite.md)), with documented, more cautious differences ([CHANGELOG](CHANGELOG.md)). Volume states are computed for the configuration format used by the qualified card; volumes described in the older format get a RAID level but an `unknown` state.

The cost of this approach:
- Each driver family needs its own module, written and qualified on real hardware, where a vendor tool usually covers many models at once.
- For `aacraid`, the management interface is not publicly documented. Each model is qualified on real hardware; cards of other models are not read beyond a few identification commands unless the operator allows it (`-allow-unqualified`, see below).
- Today only one card is qualified.

## What it monitors

`aacraid` module on the qualified Adaptec 6805. Full list of metrics and labels: [docs/metrics.md](docs/metrics.md).

| Object | Published | Observed on hardware | Covered by tests on constructed data only |
|---|---|---|---|
| Controller | identity (model, firmware, serial, PCI address), driver and module, temperature and its thresholds | identity, temperature and thresholds | — |
| Volumes | RAID level, state, capacity, members, write-cache policy and actual write-cache state | optimal state; RAID 1 mirrors and an assembly of mirrors; with a failed ZMM, write cache on under `wb`, off under `wbb` and `wt` | degraded, rebuilding, failed and reduced-redundancy states; RAID 0, 1E, 5, 5EE, 6, 10, 50, 60, simple volumes, spanned volumes made of disks |
| Disks | state, inventory (vendor, model, firmware, serial, WWN), capacity, location on the controller, Linux device | online state, inventory, location, Linux device | rebuilding, failed, ready, spare, copyback, passthrough and unsupported states |
| Background tasks | progress of rebuild, verification and other tasks per volume | no task running | every task type and progress (reports never captured yet: the next rebuild on the reference host is the hardware check) |
| Cache protection | presence, type and state of the module (ZMM, or classic battery) | failed ZMM state | charging and optimal ZMM states; every battery state, including `maintenance` |
| Collection | success and last success time per collector, collection interval | successful collection | — |

Not yet observed on hardware: the write cache turning on again under `wbb` once the ZMM is optimal. Not yet available: overall controller state and controller statistics.

Reading levels ([details](docs/aacraid-interface.md#niveaux-de-lecture-dune-carte)):
- **Validated model and firmware** (Adaptec 6805, 5.3-0[19204]): everything above, with two differences from the vendor tool kept for this card only: reading only the chunks it needs (accepted by this card's firmware) and one re-read after a rejected close.
- **Qualified model, other firmware**: read with the vendor tool's command sequence.
- **Another model**: listed with its identity; the module only checks that its device node designates the card and sends it no firmware command; its collectors are published as not supported (`hwraid_collector_supported 0`). With `-allow-unqualified=aacraid` (off by default), or `-allow-unqualified=serial:<number>` for that card alone, it is read like the previous case. The commands are then those of the vendor tool, checked only against constructed answers on such a model: the operator accepts that risk ([deployment](docs/deploiement.md#cartes-non-qualifiées)). `hwraid_controller_qualified` tells whether the model is qualified; the `firmware` label of `hwraid_controller_info`, compared with the table of qualified models, tells whether the pair is validated.

The [capture guide](docs/guide-capture.md) explains how to record the exchanges needed to qualify a model.

## Architecture

The exporter separates a driver-independent core from one module per driver family. Design contract: [CDC §6.10](cdc/cahier-des-charges-aacraid-exporter.md#610-contrat-dun-module-de-collecte).

```text
            sysfs: PCI functions, SCSI hosts, drivers
                              │
┌─────────────────────────────▼─────────────────────────────┐
│ core                                                      │
│  discovery ─▶ module selection ─▶ node creation (root)    │
│  ─▶ privilege drop ─▶ collection loop ─▶ snapshot ─▶ HTTP │
└──────────┬──────────────────────────────────────┬─────────┘
           │ module contract                      │
┌──────────▼──────────┐                ┌──────────▼──────────┐
│ aacraid module      │                │ candidate modules   │
│ (implemented)       │                │ megaraid_sas, hpsa, │
│                     │                │ smartpqi (no code)  │
└─────────────────────┘                └─────────────────────┘
```

- **The core** finds the controllers in sysfs, selects the module for each card, creates the device nodes the modules declare, drops privileges, runs the collection loop and serves the snapshot.
- **A module** declares, through pure functions, which cards it handles (`Match`), which device nodes it needs (`Nodes`) and which capabilities it must keep (`Capabilities`). It never creates device nodes itself. After the privilege drop, `Collect` reads the card, decodes the answers, translates vendor states into the shared vocabularies and reports the functions the card does not support.
- **Several families on one host.** A card claimed by exactly one module is assigned to it, and collected once its device nodes are prepared; a card claimed by several is reported as an error. The process keeps the union of the capabilities of the modules that have at least one card with its nodes prepared. A RAID controller no module handles is still published, with `hwraid_controller_backend_supported 0`.
- **Enforced boundaries.** An architecture test allows `ioctl` only in the transport package, and the transport refuses any command outside the read-only allowlist.

Only the `aacraid` module exists. MegaRAID, Smart Array and Smart Storage are candidate modules listed in the [specification](cdc/cahier-des-charges-aacraid-exporter.md#11-couverture-et-sélection-du-module).

## How it works

1. **Discovery.** The exporter reads `/sys` to find the RAID controllers and the driver that handles each one.
2. **Preparation, as root.** It creates its own character nodes in `/dev/hwraid_exporter`, for the cards found and only for them. It never touches the nodes created by the vendor tools.
3. **Privilege drop.** The process switches to UID/GID 65534 with no supplementary groups. It keeps only the capabilities the driver requires (`CAP_SYS_RAWIO` for `aacraid`) and sets `no_new_privs`. Every thread is checked before collection starts.
4. **Collection.** Only commands on a read-only allowlist are sent to the driver. Results are kept in a snapshot that `/metrics` serves, so a scrape never triggers a controller command.

## Build

Requires Go (version in `go.mod`). The binary is static and has no cgo.

```bash
CGO_ENABLED=0 go build -o hwraid-exporter ./cmd/hwraid-exporter
```

## Usage

List the cards as `serve` sees them (module, identity, whether it would read them), without root and without sending any command:

```bash
./hwraid-exporter discover
```

Collect once and print the result, as Prometheus text or as a readable summary of the main normalized values ([deployment](docs/deploiement.md#collecte-unique-oneshot)):

```bash
sudo ./hwraid-exporter oneshot -format text
```

Run the exporter. It must start as root for the preparation phase:

```bash
sudo ./hwraid-exporter serve -listen 127.0.0.1:9799 -interval 1m
```

| Option | Default | Meaning |
|---|---|---|
| `-listen` | `127.0.0.1:9799` | HTTP listen address |
| `-interval` | `1m` | Collection interval (controller commands are sent at this pace, not at scrape time) |
| `-collect-timeout` | smaller of `30s` and `-interval` | Maximum time to wait for one card in a cycle. A card that does not answer in time is published with failed collectors; the others are not delayed. A card stuck in a controller command is not queried again until that command returns. |
| `-uid`, `-gid` | `65534` | Identity after the privilege drop (root refused) |
| `-sysfs` | `/sys` | sysfs root (tests) |

## Docker

No component is needed on the host. Reference Compose profile, validated on the reference host (Debian 11, kernel 5.10):

```yaml
services:
  hwraid-exporter:
    image: ghcr.io/disaster/hwraid_exporter:v0.4.0
    read_only: true
    cap_drop: [ALL]
    cap_add: [MKNOD, CHOWN, SETUID, SETGID, SETPCAP, SYS_RAWIO]
    device_cgroup_rules:
      - "c *:* r"
    security_opt:
      - no-new-privileges:true
    # The image listens on all interfaces (the native binary defaults to
    # 127.0.0.1): attach the container to Prometheus' network, or publish the
    # port behind a firewall.
    command: ["serve", "-listen", "0.0.0.0:9799"]
    restart: unless-stopped
    # On SIGTERM the exporter waits, with no limit of its own, for a card
    # command already sent: killing it during one makes the aacraid driver
    # lose a command block until the next reboot
    # (docs/aacraid-defaut-pilote.md). This is the only limit; Docker's
    # default is 10 s.
    stop_grace_period: 30s
```

Stop the container with `docker compose down` (which applies `stop_grace_period`) or `docker stop -t 30 <container>`, never `docker rm -f` or `docker kill`. The deployment guide also applies the exporter's seccomp profile (`deploy/docker/seccomp.json`), which removes `iopl` and `ioperm` from Docker's default profile. When upgrading from v0.1.1, the old process is the one being stopped, without this protection: see [upgrading from v0.1.1](docs/deploiement.md#mise-à-jour-depuis-la-v011).

The nodes are created in the container's own `/dev`, not on the host. Why each capability is needed: [CDC §6.5](cdc/cahier-des-charges-aacraid-exporter.md). Full deployment guide, including the systemd unit for native installs: [docs/deploiement.md](docs/deploiement.md).

## Documentation

- [Specification (CDC)](cdc/cahier-des-charges-aacraid-exporter.md): requirements, security model, metrics contract, test plan.
- [aacraid interface](docs/aacraid-interface.md): commands and response formats used by the exporter, with the provenance of each item.
- [Deployment and configuration](docs/deploiement.md): Docker, systemd, options, Prometheus.
- [aacraid driver defect](docs/aacraid-defaut-pilote.md): the Linux driver loses a management command block each time a signal interrupts a command; consequences, what the exporter does about it, how to detect a leak ([`tools/aacraid-fib-trace.sh`](tools/aacraid-fib-trace.sh)).
- [Metrics](docs/metrics.md): every exposed metric, its labels and the normalized vocabularies shared by all drivers.
- [Alert rules](docs/alerts.md): example Prometheus rules, tested with `promtool` ([rules](deploy/prometheus/hwraid-exporter.rules.yml)).
- [Legal basis](docs/base-legale-interoperabilite.md): why information not documented by the vendor can be used and published (interoperability exception).
- [Capture guide](docs/guide-capture.md): how to capture the exchanges of a card the exporter does not support yet; [`aacraid` annex](docs/guide-capture-aacraid.md) with [`tools/fibcap`](tools/fibcap).

The project documentation is written in French.

## Development

```bash
go vet ./...
go test -race ./...
sh scripts/check-public.sh
```

Some tests need root (privileges, node creation). They run in CI with `HWRAID_REQUIRE_ROOT_TESTS=1`. `scripts/check-public.sh` runs as a pre-commit hook and blocks anything from the internal analysis.

## Security

Report vulnerabilities privately: see [SECURITY.md](SECURITY.md).

## License

Apache License 2.0: see [LICENSE](LICENSE) and [NOTICE](NOTICE). Release binaries and container images ship the licences of the third-party code they include (`/licenses` in the image, `*-licenses.tar.gz` next to the binary).
