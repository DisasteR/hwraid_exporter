# hwraid_exporter

Prometheus exporter for hardware RAID controllers. It talks directly to the Linux drivers and needs no vendor tool (ARCCONF, StorCLI, ssacli) at runtime.

> **Status: first release (v0.1.0).** First target: Adaptec 6805 (`aacraid` driver), validated on real hardware: volume and disk states, RAID levels, disk inventory, location and Linux device, cache protection (ZMM) state. Not yet available: task progress (rebuild), write-cache state, controller statistics.

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
    image: ghcr.io/disaster/hwraid_exporter:v0.1.0
    read_only: true
    cap_drop: [ALL]
    cap_add: [MKNOD, CHOWN, SETUID, SETGID, SETPCAP, SYS_RAWIO]
    device_cgroup_rules:
      - "c *:* r"
    security_opt:
      - no-new-privileges:true
    # The default listen address (127.0.0.1) is unreachable from outside the
    # container: attach it to Prometheus' network, or publish the port behind
    # a firewall.
    command: ["serve", "-listen", "0.0.0.0:9799"]
    restart: unless-stopped
```

The nodes are created in the container's own `/dev`, not on the host. Why each capability is needed: [CDC §6.5](cdc/cahier-des-charges-aacraid-exporter.md). Full deployment guide, including the systemd unit for native installs: [docs/deploiement.md](docs/deploiement.md).

## Documentation

- [Specification (CDC)](cdc/cahier-des-charges-aacraid-exporter.md): requirements, security model, metrics contract, test plan.
- [aacraid interface](docs/aacraid-interface.md): commands and response formats used by the exporter, with the provenance of each item.
- [Deployment and configuration](docs/deploiement.md): Docker, systemd, options, Prometheus.
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
