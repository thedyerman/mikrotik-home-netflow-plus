# mikrotik-home-netflow-plus

Live network traffic for a MikroTik router, grouped per connection and per device, in a web interface you host yourself.

One Go binary, one container, one SQLite file. No cloud service, no agents on your devices.

**Ready-made Docker image:** [`kcdyer/mikrotik-home-netflow-plus`](https://hub.docker.com/r/kcdyer/mikrotik-home-netflow-plus) on Docker Hub, public, built for amd64, arm64 and 32-bit ARM (ARMv7). Nothing to compile.

```sh
docker pull kcdyer/mikrotik-home-netflow-plus:latest
```

- See what is using the connection **right now**, second by second.
- See which **device** talked to which **destination** over the last hour, day, week or month.
- Put a **wall display** on a small screen that shows the network at a glance.
- Get **alerts** for new devices, unusual uploads, scan-like behaviour and new VPN tunnels.

---

## Contents

- [How it works](#how-it-works)
- [Features](#features)
- [Requirements](#requirements)
- [Quick start](#quick-start)
- [Setup guides](#setup-guides)
- [The web interface](#the-web-interface)
- [Wall display](#wall-display)
- [Alerts](#alerts)
- [Configuration](#configuration)
- [Data and retention](#data-and-retention)
- [Security and privacy](#security-and-privacy)
- [Limitations](#limitations)
- [Troubleshooting](#troubleshooting)
- [Building and publishing](#building-and-publishing)
- [Development](#development)
- [Project layout](#project-layout)
- [Acknowledgements](#acknowledgements)
- [Licence](#licence)

---

## How it works

The collector combines two sources from the router. Their counters are never mixed.

| | Accounting lane | Live lane (optional) |
|---|---|---|
| Source | IPFIX flow records the router pushes over UDP | The RouterOS API, polled with a read-only user |
| Delay | 15–75 seconds | 1–2 seconds |
| Gives you | History, totals, rankings, the flow map, alerts | Current rates, the active connection table, device names, DNS names |

```
 MikroTik router ── IPFIX, UDP 2055 ──────────────►┐
        ▲                                          │   mikrotik-home-netflow-plus
        └────────── RouterOS API, TLS 8729 ◄───────┤   decode → attribute → store → serve
                                                   │
 Browser / wall display ◄── HTTP + WebSocket, 8080 ┘
```

RouterOS cannot export an active flow more often than once a minute, so flow records alone are always a little behind. The live lane fills that gap. Without it the app still works from flow records; live views then show a 90-second average and say so.

FastTracked traffic **is** included in the flow export. In testing, flow records accounted for about 99% of the bytes on the WAN interface, and the app shows that figure continuously as *flow coverage*.

The design, and the RouterOS behaviour it was built around, are described in [docs/DESIGN.md](docs/DESIGN.md).

## Features

**Live**
- WAN throughput chart with one sample per second, download above and upload mirrored below.
- Active connections with current rates, grouped by connection, device, destination or service.
- Top devices, destinations and services by current rate.

**History**
- Throughput and rankings over 15 minutes, 1 hour, 6 hours, 24 hours, 7 days and 30 days.
- Searchable connection history (kept 48 hours by default).
- A Sankey flow map: devices → services → destinations.
- A page per device: timeline, destinations, services, active connections, alerts.

**Understanding the traffic**
- Devices are identified by MAC address, so a changed IP address does not split a device in two. Names come from DHCP, and you can rename a device inline.
- Destinations are named from the router's DNS cache and grouped by registered domain; anything without a name gets the organisation that owns the address (for example "Cloudflare").
- NAT is resolved: traffic is attributed to the LAN device, not the router's WAN address.
- Traffic is split into zones: internet, remote sites reached over tunnels, and traffic to the router itself.
- VPN and overlay protocols (IPsec, WireGuard, OpenVPN, Tailscale, ZeroTier) are flagged as tunnels.

**Operations**
- Nine alert rules with a JSON webhook (works with ntfy, Slack, Discord, Home Assistant).
- A Status page showing export health, lost records, clock offset, flow coverage and storage use.
- A full-screen wall display at `/dashboard` for a small always-on screen.
- Optional password for the web interface.
- Light and dark themes.

## Requirements

| What | Details |
|---|---|
| Router | MikroTik with **RouterOS 7**. Developed against 7.20 on an L009; any device that supports Traffic Flow should work |
| Host | Any Linux machine with Docker and Compose v2, on the same network as the router. The public image on Docker Hub covers amd64, arm64 and ARMv7, so a Raspberry Pi works as well as an x86 server |
| Resources | Well under 200 MB RAM and negligible CPU for a home network; 2 GB of disk by default |
| Ports | UDP 2055 (flow records in), TCP 8080 (web interface). Both configurable |

The router's own CPU cost was not measurable in testing.

## Quick start

On the Linux host, with Docker and Compose v2 installed:

```sh
git clone https://github.com/thedyerman/mikrotik-home-netflow-plus.git
cd mikrotik-home-netflow-plus

cp deploy/env.example deploy/.env
chmod 600 deploy/.env
# edit deploy/.env: the router's address, and the API user and password

docker compose -f deploy/docker-compose.yml up -d
```

This pulls the published image, [`kcdyer/mikrotik-home-netflow-plus`](https://hub.docker.com/r/kcdyer/mikrotik-home-netflow-plus) on Docker Hub, which is built for amd64, arm64 and ARMv7. Nothing needs compiling.

Then configure the router so it sends flow records to this host: see [SETUP-MIKROTIK-ROUTER.md](SETUP-MIKROTIK-ROUTER.md). The short version, from a RouterOS terminal:

```
/ip traffic-flow set enabled=yes active-flow-timeout=1m
/ip traffic-flow target add dst-address=<COLLECTOR_IP> port=2055 version=ipfix
```

Open `http://<host>:8080`. The **Status** page tells you whether flow records are arriving and shows the complete router script with your addresses filled in.

The compose file uses host networking, so flow packets arrive with the router's real source address and no port mapping is needed.

## Setup guides

| Guide | What it covers |
|---|---|
| [SETUP-MIKROTIK-ROUTER.md](SETUP-MIKROTIK-ROUTER.md) | Flow export, the read-only API user, the API certificate, verification, troubleshooting and how to undo it all |
| [SETUP-SIMPLEDOCKEROPS.md](SETUP-SIMPLEDOCKEROPS.md) | Deploying and upgrading the collector on a server managed with [Simple Docker Ops](https://simpledockerops.com) |
| [SETUP-SIMPLEDISPLAYOPS.md](SETUP-SIMPLEDISPLAYOPS.md) | Showing the wall display on a Raspberry Pi screen managed with [DisplayOps](https://simpledisplayops.com) |
| [docs/DESIGN.md](docs/DESIGN.md) | Architecture, measured RouterOS behaviour, storage model and design decisions |

Neither hosted service is required. Plain Docker Compose runs the collector, and any browser in kiosk mode can show the wall display.

## The web interface

| View | What it shows |
|---|---|
| **Overview** | Live throughput and the top devices, destinations and services; or the same over a historical range, with totals and peaks. The chart has a table view |
| **Flows** | *Live:* connections moving data now, with rates, filter and grouping. *History:* stored connections, searchable by name, address or port |
| **Devices** | Every device with its current rate, volume, trend and last-seen time. Rename inline |
| **Device page** | One device: throughput timeline, top destinations and services, traffic by zone, active connections, flow map, alerts |
| **Flow map** | Sankey diagram from devices through services to destinations. Click a node to see its connections |
| **Alerts** | Event feed, rule settings and the notification webhook |
| **Status** | Flow export health, router API state, flow coverage, how destinations are being named, network classification, storage, and the router setup script |

A zone selector scopes the historical views: *External* (internet and remote sites, the default), *Internet*, *Sites*, *Local*, or *All*.

## Wall display

`/dashboard` is a single full-screen page for an always-on display. It is always dark, has no controls and hides the pointer.

It shows download and upload right now, a live throughput chart, top devices, top destinations, today's totals, and a status strip that turns into a banner when an alert fires, the router stops sending flow records, or the collector cannot be reached.

| URL | Result |
|---|---|
| `/dashboard` | Everything at once. Fills whatever screen it is on; sized for 800×480, 1024×768 and 1920×1080 and anything in between |
| `/dashboard?rotate=true` | Larger type. The lower panel cycles through top devices, top destinations and today's totals |
| `/dashboard?rotate=true&interval=20` | Seconds per panel when rotating (default 12, minimum 4) |
| `/dashboard?resolution=800x480` | Lays out for exactly that size and scales it to fit the window. Useful for previewing a small screen on a desktop. Also accepts any `WIDTHxHEIGHT`, `1080p` and `720p` |

The page updates over a WebSocket, reconnects by itself, and reloads when the collector is upgraded, so no refresh interval is needed.

If a web password is set, `/dashboard` asks for it with HTTP basic auth (any user name) instead of the login form, so an unattended kiosk can answer with a stored credential.

Step-by-step instructions for a Raspberry Pi screen are in [SETUP-SIMPLEDISPLAYOPS.md](SETUP-SIMPLEDISPLAYOPS.md).

## Alerts

| Rule | Fires when | Default |
|---|---|---|
| New device | A device never seen before sends traffic | on |
| Sustained throughput | A device stays above a rate for several minutes in a row | off (100 Mb/s for 10 min) |
| Unusual upload | A device uploads more than a set volume to the internet in a time window | on (10 GB in 24 h) |
| Fan-out / scan pattern | A device contacts many distinct hosts in a short time | on (250 hosts in 60 s) |
| New tunnel | A device starts using a VPN or tunnel service it has not used before | on |
| Flow export stopped | The router has sent no flow records for a while | on (5 min) |
| Router API unreachable | The live lane is down | on (5 min) |
| Flow coverage low | Flow records account for less of the WAN traffic than expected | on (below 85% for 10 min) |
| Router rebooted | The router's boot time changed | on |

Thresholds are edited on the Alerts page. Set a webhook URL there and every event is also sent as JSON; the payload carries `text` and `content` fields so Slack- and Discord-style webhooks display it, and a `Title` header for ntfy.

For the first 20 minutes after first start, new-device and new-tunnel events are suppressed while the collector learns the network.

## Configuration

All settings are environment variables. Any secret can also be supplied as `<NAME>_FILE`, pointing at a file that contains the value.

### Router

| Variable | Purpose | Default |
|---|---|---|
| `NFP_EXPORTERS` | Addresses allowed to send flow records (comma separated) | the router API host; if neither is set, any source |
| `NFP_ROUTER_ADDR` | Router API host or `host:port`. Empty disables the live lane | empty |
| `NFP_ROUTER_USER` | Read-only API user | |
| `NFP_ROUTER_PASSWORD` | Its password | |
| `NFP_ROUTER_TLS` | `true`: api-ssl on 8729. `false`: plain api on 8728 | `true` |
| `NFP_ROUTER_CERT_FINGERPRINT` | SHA-256 of the router's API certificate | pinned on first use, stored in `/data/router-cert.pin` |
| `NFP_ACTIVE_FLOW_TIMEOUT` | Must match the router's `active-flow-timeout` | `1m` |

### Network

Discovered from the router when the live lane is on. Set these to override, or when running from flow records only.

| Variable | Purpose | Default |
|---|---|---|
| `NFP_LOCAL_NETS` | Local networks, as a CIDR list. Do not include 100.64.0.0/10: that is what a carrier-NAT WAN address looks like | discovered, else the private ranges |
| `NFP_SITES` | Named remote sites, `name=cidr,...` | networks routed over tunnel interfaces |
| `NFP_WAN_INTERFACES` | WAN interface names | default-route interfaces and the `WAN` interface list |

### Service

| Variable | Purpose | Default |
|---|---|---|
| `NFP_HTTP_LISTEN` | Web interface listen address | `:8080` |
| `NFP_FLOW_LISTEN` | Flow record listen address (UDP) | `:2055` |
| `NFP_AUTH_PASSWORD` | Require this password for the web interface | no login |
| `NFP_DATA_DIR` | Database and state | `/data` |
| `NFP_LOG_LEVEL` | `debug`, `info`, `warn` or `error` | `info` |

### Storage

| Variable | Purpose | Default |
|---|---|---|
| `NFP_RETENTION_CONNECTIONS` | How long individual connections are kept | `48h` |
| `NFP_RETENTION_1M` | How long minute rollups are kept | `7d` |
| `NFP_RETENTION_1H` | How long hourly rollups are kept | `30d` |
| `NFP_FOLD_BELOW` | Connections smaller than this are counted but not stored one by one | `10KB` |
| `NFP_FLUSH_INTERVAL` | How often accumulated data is written to the database. Use `60s` on an SD card; see below | `20s` |
| `NFP_DB_MAX_SIZE` | Database size cap; the oldest fine-grained data is dropped first | `2GB` |
| `NFP_ASN_DB` | Path to an ASN database in MMDB format | the copy baked into the image |

## Data and retention

Everything lives in one SQLite file on the `/data` volume.

| Tier | Kept | Used for |
|---|---|---|
| Per-second live samples | 20 minutes, in memory | Live chart and coverage |
| Individual connections of 10 kB or more | 48 hours | Flow history |
| Minute rollups per device, destination, service and zone | 7 days | Charts and rankings up to 24 hours |
| Hourly rollups | 30 days | Week and month views |

Most connections are tiny: in testing about nine in ten were under 10 kB and together carried well under 1% of the bytes. They are counted in the totals but not stored individually.

To back up, copy the volume while the container is stopped, or copy `netflow.db` together with its `-wal` file.

### Flush interval and SD cards

Live views are served from memory. The database is only written in batches, every `NFP_FLUSH_INTERVAL` (20 seconds by default), and that interval decides two things: how soon new data shows up in the historical views, on top of the 15–75 seconds the router itself takes to export a flow, and how much accounting is lost on a power cut.

It also decides how much is written to disk. Every flush rewrites the same set of 4 kB database pages, so flushing three times less often writes roughly three times fewer bytes. Measured on a home network, a 5-second interval wrote about 4 GB a day against a 13 MB database; 20 seconds brings that to about 1.5 GB and 60 seconds to under 1 GB.

**On a Raspberry Pi or anything else running from an SD card, set `NFP_FLUSH_INTERVAL=60s`.** SD cards have a limited number of write cycles and no wear-levelling worth the name, so the fewer bytes rewritten, the longer the card lasts. The cost is that history views lag up to a minute longer; the live views do not change. On an SSD or a NAS the default is fine.

### Running from a ramdisk

Putting `/data` on a ramdisk removes flash wear entirely and makes every query fast. The trade is durability, and it has to be understood before choosing this:

> **Everything in `/data` is lost when the host reboots or loses power:** all history, the device names you typed in, acknowledged alerts, the pinned router certificate. The collector starts again empty, re-learns device names from DHCP within a minute, and the history starts from zero. Only use a ramdisk if losing the history on a reboot is acceptable, or together with the backup below.

#### Size

A ramdisk only uses what is stored, so the size is a ceiling. Measured on a home network with about 25 devices:

| Retention | Settings | Expected database | Ramdisk size |
|---|---|---|---|
| Default: 7 days of minute data, 30 days of hourly data, 48 hours of connections | none | about 125 MB | **256 MB** |
| Everything for 30 days | `NFP_RETENTION_1M=30d`, `NFP_RETENTION_CONNECTIONS=30d` | about 550 MB | **1 GB** |

Size grows with the number of devices and how many different destinations they talk to, not with bandwidth: roughly 0.7 MB per device per day while data is being retained. A network with 60 devices keeping everything for 30 days needs about 2 GB.

**Always set `NFP_DB_MAX_SIZE` below the ramdisk size**, for example `700MB` on a 1 GB ramdisk. The collector then prunes its oldest data before the ramdisk can fill up. The default cap of 2 GB is larger than these ramdisks.

#### Setup

Create the ramdisk on the host rather than letting Docker create it. A Docker-managed `tmpfs` is tied to the container, so it would also be wiped every time the container is recreated, which is every upgrade. A host ramdisk survives container upgrades and is lost only on a host reboot. Add to `/etc/fstab`:

```
tmpfs  /mnt/netflow-ram  tmpfs  size=1g,mode=1777  0  0
```

Mount it (`sudo mkdir -p /mnt/netflow-ram && sudo mount /mnt/netflow-ram`) and bind it into the container in place of the named volume:

```yaml
    volumes:
      - /mnt/netflow-ram:/data
```

`mode=1777` lets the container's non-root user write to it. Set `NFP_ROUTER_CERT_FINGERPRINT` explicitly: the certificate pin normally lives in `/data`, and re-learning it after every reboot would defeat its purpose.

#### Backing up the ramdisk to flash

An hourly copy to flash turns "lose everything on reboot" into "lose up to an hour". `sqlite3` makes a consistent copy of a database that is in use, so install it on the host (`apt install sqlite3`) and add a cron entry:

```
0 * * * *  sqlite3 /mnt/netflow-ram/netflow.db ".backup '/var/lib/netflow-backup/netflow.db'"
```

Do not use `cp` for this: copying a live database without its write-ahead log can produce a corrupt file.

To restore at boot, the copy must be put back **before the container starts**, or the collector will create a fresh database and the restore would overwrite a live one. A `systemd` unit ordered before Docker does that:

```ini
# /etc/systemd/system/netflow-restore.service
[Unit]
Description=Restore the netflow database into the ramdisk
Before=docker.service
RequiresMountsFor=/mnt/netflow-ram
ConditionPathExists=/var/lib/netflow-backup/netflow.db

[Service]
Type=oneshot
ExecStart=/bin/cp /var/lib/netflow-backup/netflow.db /mnt/netflow-ram/netflow.db

[Install]
WantedBy=multi-user.target
```

Enable it with `sudo systemctl enable netflow-restore.service`. The hourly backup writes about 125 MB to 550 MB of flash each time, depending on the database size, which is still far less than the continuous writes the ramdisk avoids.

## Security and privacy

This tool records what every device on your network connects to. Treat the data accordingly.

- **It stays local.** The collector makes no outbound connections except to your router and to a webhook you configure. The organisation database is baked into the image at build time.
- **The router account is read-only**, with only the `read` and `api` policies, and restricted to the collector's address on the router. The collector never needs an administrative account.
- **The API session is encrypted** and the router's certificate is pinned on first use. A changed certificate is refused until you remove the pin.
- **Flow packets are accepted only from configured exporters.**
- **The web interface has no login by default.** Set `NFP_AUTH_PASSWORD` if anyone else can reach the host, and put a reverse proxy with TLS in front of it if it is exposed beyond your LAN.
- **Keep secrets out of version control.** `deploy/.env` is git-ignored. Prefer `NFP_ROUTER_PASSWORD_FILE` or your platform's secret store.

## Limitations

- Traffic between two devices on the same LAN segment is switched in hardware and never reaches the router's flow export. It is not shown.
- Hardware-offloaded routing (layer-3 offload on some switch and router models) is invisible to Traffic Flow. The coverage figure will reveal this.
- The contents of VPN tunnels are opaque. A tunnel appears as one long connection, flagged with a lock.
- Destination names depend on devices using the router as their DNS resolver. Browsers with DNS over HTTPS bypass it; those destinations are labelled by organisation instead.
- Flow records for a long-running connection arrive about every 75 seconds. Only the live lane is faster.
- One router per collector is what has been tested.

## Troubleshooting

| Symptom | Likely cause and fix |
|---|---|
| Status shows "No flow records received yet" | The router target is wrong or missing. Check `/ip traffic-flow target print` and that `dst-address` is this host. Check that nothing blocks UDP 2055 |
| Flow packets are "rejected from other sources" | The router sends from an address not in `NFP_EXPORTERS`. Set `src-address` on the target, or add the address |
| The container restarts in a loop | Read `docker logs`. The usual cause is a port already in use: set `NFP_HTTP_LISTEN` to a free port |
| Router API: "TLS handshake failed" | `api-ssl` has no certificate. See [the router guide](SETUP-MIKROTIK-ROUTER.md#3-give-the-api-a-certificate) |
| Router API: "connection refused" | The `api-ssl` service is disabled, or its `address` list does not include the collector |
| Router API: "invalid user name or password" | Wrong credentials, or the user's `address` restriction does not match the collector |
| Router API: "certificate changed" | The router's certificate was replaced. Delete `router-cert.pin` from the data volume, or set `NFP_ROUTER_CERT_FINGERPRINT` |
| Devices show as addresses instead of names | The live lane is off, or the device has no DHCP host name. Rename it on the Devices page |
| Destinations show as organisations, not domains | The device does not resolve through the router (DNS over HTTPS, or a hard-coded resolver) |
| Coverage is well below 95% | Export packets are being lost, the router's flow cache is full (`cache-entries`), or hardware offload is on |
| The "Live" indicator says "Delayed" | The router API is not configured or not reachable; the Status page shows the error |

## Building and publishing

You only need this to run a modified version. The published image, [`kcdyer/mikrotik-home-netflow-plus`](https://hub.docker.com/r/kcdyer/mikrotik-home-netflow-plus), covers normal use; `latest` follows the newest release and each release is also tagged with its version number.

The Dockerfile builds the web interface, downloads the organisation database and compiles a static binary into a distroless image that runs as a non-root user.

```sh
# a local image for this machine
docker build --build-arg VERSION=1.1.1 -t mikrotik-home-netflow-plus:dev .

# a multi-architecture image, pushed to a registry
docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 --build-arg VERSION=1.1.1 \
  -t <registry>/<namespace>/mikrotik-home-netflow-plus:1.1.1 --push .
```

Then put that image name in the compose file. The build stages cross-compile, so building for all three architectures needs no emulation.

The image is about 12 MB to download (40 MB unpacked). It has a built-in health check (`/mikrotik-home-netflow-plus healthcheck`), so orchestrators can tell when it is ready.

## Development

Requires Go 1.26 or newer and Node 22 or newer.

```sh
cd web && npm ci && npm run build && cd ..   # build the interface into web/dist; it is embedded in the binary
go test ./...                                # replays a real RouterOS capture through the pipeline
scripts/dev-run.sh --fresh                   # collector on 127.0.0.1:8080, flow records on 127.0.0.1:2055
./.devdata/nfp replay testdata/captures/routeros7-ipfix-sample.bin 127.0.0.1:2055
```

For interface work, run `npm run dev` in `web/`: Vite serves the interface with hot reload and proxies the API to the collector on port 8080. Set `NFP_DEV_API` to proxy to a collector elsewhere.

To run against a real router during development, put `NFP_*` settings in `.devdata/dev.env` (git-ignored).

### Tests

The tests replay `testdata/captures/routeros7-ipfix-sample.bin`, a capture of real RouterOS 7 export with every address anonymised, together with a ground-truth file describing three transfers of known size and the router's interface counters. They check that:

- the decoder reads every record and loses none;
- the three transfers come out at the right size, attributed to the right device, at the right time;
- flow totals match the interface counters to about 99%;
- every byte of every flow ends up in the rollups, no more and no less;
- internet hosts are never mistaken for the router.

`scripts/anonymize-capture.py` is the tool that produced the sample. Use it before sharing a capture of your own: see [testdata/captures/README.md](testdata/captures/README.md).

`scripts/update-oui.py` refreshes the embedded MAC vendor table from the IEEE registries.

## Project layout

```
cmd/mikrotik-home-netflow-plus   entry point, UDP listener, wiring
internal/ipfix                   IPFIX decoder and template cache
internal/flow                    clock anchoring, NAT inside view, zones
internal/routeros                RouterOS API client and poller
internal/enrich                  services, MAC vendors, DNS names, organisations
internal/engine                  devices, connection stitching, rollups, live state, coverage
internal/store                   SQLite schema, batch writes, queries, retention
internal/alert                   rules, events, webhook
internal/api                     HTTP API, WebSocket feed, authentication, static files
internal/config                  environment configuration
web                              React interface (Vite, TypeScript)
deploy                           compose files, env example, router script
docs                             design notes
scripts                          development helpers
testdata/captures                anonymised sample capture and ground truth
```

## Acknowledgements

- Organisation labels use IP to ASN Lite data by [DB-IP](https://db-ip.com), licensed CC BY 4.0. It is downloaded when the image is built.
- MAC vendor names come from the IEEE Registration Authority's public listings.
- Charts are drawn with [uPlot](https://github.com/leeoniya/uPlot); the flow map layout is [d3-sankey](https://github.com/d3/d3-sankey).
- Storage is [SQLite](https://sqlite.org) through [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite).
- The wall display is set in [Inter](https://rsms.me/inter/).

See [NOTICE](NOTICE) for the full list of third-party components and their licences.

## Licence

Apache License 2.0. See [LICENSE](LICENSE).

This project is independent. It is not affiliated with or endorsed by MikroTik, Simple Docker Ops or DisplayOps. MikroTik and RouterOS are trademarks of Mikrotikls SIA.
