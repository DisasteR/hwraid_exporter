# hwraid_exporter

Prometheus exporter for hardware RAID controllers. It talks directly to the Linux drivers and needs no vendor tool (ARCCONF, StorCLI, ssacli) at runtime.

> **Status: v0.1.2.** First target: Adaptec 6805 (`aacraid` driver). Validated on real hardware: optimal volume and disk states, RAID levels, disk inventory, location and Linux device, failed cache protection (ZMM), volume write-cache policy (all three settings) and state: on with the `wb` setting (capture of 2026-11-23, ZMM failed), off with `wbb` and `wt`. The degraded, rebuilding and failed volume states and the charging and optimal ZMM states are covered by tests on constructed data only, not yet observed on hardware. The write cache turning on again under `wbb` once the ZMM is optimal is not yet observed on hardware. Not yet available: task progress (rebuild), controller statistics.

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

List the controllers found, without root and without sending any command:

```bash
./hwraid-exporter discover
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
    image: ghcr.io/disaster/hwraid_exporter:v0.1.2
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
