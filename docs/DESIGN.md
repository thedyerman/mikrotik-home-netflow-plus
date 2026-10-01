# Design notes

How mikrotik-home-netflow-plus works, and the RouterOS behaviour it was built around.

The measurements in this document were taken on a MikroTik L009 running RouterOS 7.20 (long-term) on a small home network: one bridged LAN, one WAN behind carrier-grade NAT, FastTrack enabled, and a WireGuard site-to-site tunnel. Addresses shown are examples.

- [The problem with flow export alone](#the-problem-with-flow-export-alone)
- [Two lanes of data](#two-lanes-of-data)
- [Architecture](#architecture)
- [What RouterOS actually sends](#what-routeros-actually-sends)
- [Flow processing pipeline](#flow-processing-pipeline)
- [The live lane](#the-live-lane)
- [Naming things](#naming-things)
- [Storage](#storage)
- [Coverage](#coverage)
- [Alerts](#alerts)
- [Security and privacy](#security-and-privacy)
- [Decisions and their reasons](#decisions-and-their-reasons)
- [Known limits](#known-limits)

## The problem with flow export alone

RouterOS Traffic Flow exports NetFlow v1, v5, v9 and IPFIX over UDP. IPFIX is the useful one: it is template based and carries IPv6, MAC addresses and NAT translation fields.

Flow export is complete but not live:

- `active-flow-timeout` has a **minimum of one minute**. `inactive-flow-timeout` defaults to 15 seconds.
- Measured: every record arrives about **15 s after its last packet**, and a continuous flow is exported about every **75 s** (60 s active timeout plus 15 s).

A download that runs for ten minutes therefore shows up as a handful of records, each more than a minute apart. That is fine for history and useless for a "right now" display.

## Two lanes of data

The collector uses two sources that do different jobs. Their counters are never mixed.

| | Accounting lane | Live lane |
|---|---|---|
| Source | IPFIX flow records, pushed by the router | RouterOS API, polled by the collector |
| Latency | 15–75 s | 1–2 s |
| Completeness | Every flow the router CPU handled, including very short ones | Only what exists at poll time |
| Used for | History, totals, rankings over time, flow map, volume alerts | Current rates, the active connection table, the live chart |
| Stored | Yes | No, memory only |

If the router API is not configured or is unreachable, the collector keeps working from flow records alone. Live views then show a 90-second average of recent records and are labelled as delayed.

## Architecture

```
 MikroTik router (RouterOS 7)
   │  IPFIX over UDP :2055 (push)              ▲  RouterOS API over TLS :8729 (poll, read-only user)
   ▼                                           │
┌──────────────────────────────────────────────┴───────────────────────────────┐
│  mikrotik-home-netflow-plus (single Go binary, single container)             │
│                                                                              │
│  UDP listener                       Router poller                            │
│  IPFIX decode, template cache,      active connections 2 s                   │
│  sequence-gap tracking              interface counters 1 s                   │
│        │                            DNS cache 10 s, leases / ARP 60 s        │
│        ▼                                   │                                 │
│  Normaliser                                │                                 │
│  clock anchoring, NAT inside view,         │                                 │
│  zones                                     │                                 │
│        │                                   │                                 │
│        ▼                                   ▼                                 │
│  Engine: devices, connection stitching, rollups, live state, coverage        │
│        │                       │                       │                     │
│        ▼                       ▼                       ▼                     │
│  SQLite (connections,     Alert rules,          HTTP API, WebSocket feed,    │
│  1 m and 1 h rollups)     webhook               embedded web interface       │
└──────────────────────────────────────────────────────────────────────────────┘
```

| Package | Job |
|---|---|
| `internal/ipfix` | Template-driven IPFIX decoder, template cache on disk, record-loss detection |
| `internal/flow` | Clock anchoring, NAT inside view, zone classification |
| `internal/routeros` | RouterOS binary API client (TLS, certificate pinning) and poller |
| `internal/enrich` | Service names, MAC vendors, remembered DNS names, organisation lookup |
| `internal/engine` | Device identity, connection stitching, rollups, live rows, coverage |
| `internal/store` | SQLite schema, batched writes, queries, retention |
| `internal/alert` | Rules, event log, webhook delivery |
| `internal/api` | JSON API, WebSocket feed, authentication, static files |

## What RouterOS actually sends

Established by capturing real export and comparing it with transfers of known size and with the WAN interface counters.

| Question | Finding |
|---|---|
| Is FastTracked traffic exported? | **Yes.** A 50 MB download was reported as 51.97 MB (payload plus about 4% IP/TCP headers) |
| How complete is it? | Flow bytes matched the WAN interface counters to about **99%** in both directions |
| Format | IPFIX v10, observation domain 0, two templates: **258 for IPv4** (37 fields, 124 bytes) and **259 for IPv6** (34 fields, 152 bytes), up to 11 records per datagram |
| Sequence number | Counts records, so loss in transit is detectable |
| Duplicates | None: one record per direction per export |
| NAT, upload direction | Source is the LAN address and port; `postNATSource` is the WAN address and port |
| NAT, download direction | Destination is the **WAN address**; the real LAN host is in `postNATDestination` |
| Interface numbers | Equal to the SNMP ifIndex, which is also the hexadecimal `.id` the API reports for the interface. LAN traffic is reported against the bridge, not the physical port |
| Egress interface 0 | The packet was delivered to the router itself (or was a broadcast it received) |
| Ingress interface 0 | **Not reliable.** Usually router-originated traffic, but RouterOS also reports it on some ICMP and reply packets from internet hosts |
| Device MAC | `sourceMacAddress` is the LAN device on records that entered from the LAN. Download-direction records do not carry it |
| TCP flags | Only reflect packets seen before the connection is FastTracked (mostly SYN). They cannot be used to detect a close |
| `flowEnd` on long flows | Stamped about 15 s before export, but the **byte count covers everything up to the export moment** |
| Router clock | Cannot be trusted: a router without NTP was several seconds off |
| CPU cost on the router | Not measurable |

Traffic Flow only sees packets the router CPU handles. Traffic switched in hardware between two ports of the same bridge, or handled by layer-3 hardware offload, is invisible to it.

## Flow processing pipeline

1. **Decode.** Parse the IPFIX message; keep templates per exporter and persist them, so a restart does not have to wait for the router to resend them. Packets from addresses that are not configured exporters are dropped. Data that arrives before its template is skipped and not counted as loss.
2. **Anchor time.** The offset between the router's clock and the collector's is the minimum of `receive time − export time` over a short window (the export time is truncated to the second). Flow start and end are router uptime plus `systemInitTime`, corrected by that offset. A jump in `systemInitTime` means the router rebooted. The 32-bit uptime counter wraps every 49.7 days and is unwrapped.
3. **NAT inside view.** A record addressed to the WAN address is a download: the local side becomes `postNATDestination`. A record with a `postNATSource` different from its source is an upload. The WAN address and port are kept as metadata.
4. **Zone.** Every flow gets one of three zones: `wan` (the internet), `site` (a remote network reached over a tunnel) or `local` (the router itself, or local broadcast). The router's own internet traffic, such as the encrypted side of a WireGuard tunnel, belongs to a device representing the router.
5. **Router addresses** are learned from NAT fields, from the API, or when an address has been seen both as a source with ingress interface 0 and as a destination with egress interface 0. One side alone is not enough (see the table above).
6. **Stitch.** The two directions are merged into one connection keyed by protocol, local address and port, remote address and port. A connection ends after 150 s without records.
7. **Spread.** A record's bytes are distributed over the minutes (and seconds) it covers: `[start, end]`, or `[start, export time]` when it hit the active timeout. Without this, long transfers chart with 15-second holes.
8. **Attribute** to a device, a destination group and a service, then add to the rollups.

## The live lane

Every poll runs as a read-only user over the binary RouterOS API.

| What | Interval | Cost on the router |
|---|---|---|
| Interface byte counters | 1 s | about 10 ms |
| Connections with a non-zero rate | 2 s | about 50 ms |
| Connection count | 10 s | negligible |
| DNS cache | 10 s | under 100 ms |
| DHCP leases, ARP, IPv6 neighbours | 60 s | negligible |
| Addresses, routes, interface lists | 60 s | negligible |

Asking only for connections that are moving data matters: reading the whole connection table with every property took over a second.

The poller also discovers the topology, unless it is configured by hand: WAN interfaces are those carrying a default route or listed in an interface list named `WAN`; local networks are the ones on non-WAN, non-tunnel interfaces; networks routed over tunnel interfaces become sites.

An error reply to one query (for example a connection that vanished while the table was being read) does not end the session. Persistent ones, like missing permissions, are shown on the Status page.

## Naming things

| What | Source |
|---|---|
| Device identity | MAC address, from flow records that entered from the LAN and from DHCP leases and ARP. A device keeps its identity when its IP address changes |
| Device name | A name set in the interface, else the DHCP lease comment, else the DHCP host name, else vendor plus the end of the MAC |
| Vendor | IEEE MA-L, MA-M and MA-S registries, embedded. Randomised MACs are shown as "Private address" |
| Destination name | The router's DNS cache, polled and **remembered**: the cache only holds a record until its TTL expires, so a single snapshot names very little. CNAME chains are walked back to the name the client asked for |
| Destination group | Registered domain when a name is known (`rr4---sn-abc.googlevideo.com` becomes `googlevideo.com`), else the organisation announcing the address, else the bare address |
| Organisation | An offline IP-to-ASN database in MMDB format, baked into the image |
| Service | Protocol and port table, with VPN and overlay protocols flagged as tunnels |

DNS naming only works for clients that resolve through the router. Browsers using DNS over HTTPS and devices with hard-coded resolvers bypass it, which is why the organisation fallback exists.

## Storage

One SQLite file in WAL mode, written in batches every `NFP_FLUSH_INTERVAL` (20 s by default). Live views never read the database, so the interval only sets how soon history views see new data and how much is at risk on a power cut. Each flush rewrites the same set of pages, which is why a longer interval cuts disk writes almost proportionally: measured on a home network, 5 s wrote about 4 GB a day, 20 s about 1.5 GB and 60 s under 1 GB. SD-card hosts should use 60 s.

| Tier | Resolution | Default retention | Used for |
|---|---|---|---|
| Live | 1 s | 20 min, memory only | Live chart, coverage |
| Connections | One row per connection of 10 kB or more | 48 h | Flow history and search |
| Minute rollup | 1 min per device, destination group, service and zone | 7 days | Charts and rankings up to 24 h |
| Hourly rollup | 1 h, same key | 30 days | Week and month views |
| WAN series | 10 s interface-counter buckets | 7 days | Throughput charts |

Most connections are tiny. In the reference measurement about 89% were under 10 kB and together carried 0.6% of the bytes. Those are counted in the rollups but not stored one by one, which cuts stored rows by roughly a factor of ten without losing a byte of accounting.

A janitor enforces retention hourly and, if the database exceeds its size cap, drops the oldest fine-grained data first.

## Coverage

The collector compares the bytes accounted by flow records with the WAN interface counters over a ten-minute window ending 90 seconds ago (so records for it have arrived), adding 14 bytes of Ethernet header per packet. The result is shown as **flow coverage**. It is the permanent answer to "is the collector seeing everything?", and a sustained drop points at lost export packets, a full flow cache on the router, or hardware offload being switched on.

## Alerts

| Rule | Trigger |
|---|---|
| New device | A device never seen before sends traffic |
| Sustained throughput | A device stays above a rate for several minutes in a row (off by default) |
| Unusual upload | A device uploads more than a set volume to the internet in a time window |
| Fan-out / scan pattern | A device contacts an unusually large number of distinct hosts in a short time |
| New tunnel | A device starts using a VPN or tunnel service it has not used before |
| Flow export stopped | No flow records for a while |
| Router API unreachable | The live lane is down |
| Flow coverage low | Coverage stays below a threshold |
| Router rebooted | The router's boot time changed |

Rules that describe a condition fire and resolve; a resolved rule will not fire again for the same subject for 30 minutes. For the first 20 minutes after first start, new-device and new-tunnel events are suppressed while the collector learns the network. Every event can be delivered to a webhook as JSON.

## Security and privacy

- The data is a record of what every device on the network connects to. It stays in the container's volume. The collector makes no outbound calls except to the router and to a webhook you configure.
- The router user is read-only (`read,api`) and restricted by source address on the router. The collector never needs an administrative account.
- The API session uses TLS with the router's certificate pinned on first use; a changed certificate is refused until the pin is removed.
- Flow packets are accepted only from configured exporter addresses.
- The web interface has an optional single password. Sessions are HttpOnly, SameSite cookies; state-changing requests from other sites are refused. The wall display accepts the same password over HTTP basic auth, because an unattended screen cannot fill in a form.
- TLS for the web interface is expected to come from a reverse proxy if it is exposed beyond the LAN.

## Decisions and their reasons

| Decision | Reason |
|---|---|
| Own IPFIX decoder | RouterOS sends two fixed templates; a small decoder can be verified byte for byte against a real capture and adds no dependency |
| Own RouterOS API client | The protocol is a few dozen lines; owning it made certificate pinning and non-fatal error replies straightforward |
| Binary API, not REST | It needs no web service enabled on the router and is cheaper per query |
| SQLite through a pure-Go driver | One file, no CGO, a static binary in a distroless image |
| Rollups keyed by destination *group* | Bounds cardinality: thousands of CDN addresses collapse into one domain or organisation |
| Interface counters for the live chart | Exact and current, unlike flow records |
| Plain tables, server-side row cap | No virtualisation library needed at home-network scale |
| Flow-only live rates as a 90 s average | A per-record rate made finished bursts look active for a minute |
| Displayed rates smoothed with fast attack / slow release; rankings sticky; chart raw | One-second rates change by half from second to second on a real link, and rankings of near-equal values flip constantly. Smoothing the figures (2 s rise, 6 s fall) and requiring a clear margin before rows swap keeps the view readable without hiding real changes; the chart keeps the raw samples because a line is read as a whole |

## Known limits

- Traffic between two devices on the same LAN segment is switched in hardware and is never seen.
- The contents of a VPN tunnel are opaque. A tunnel appears as one long connection.
- Flow records for a long connection arrive about every 75 seconds; only the live lane fills that gap.
- IPv6 on the LAN is decoded and classified, but mapping rotating privacy addresses to devices depends on the router's neighbour table and has had little real-world testing.
- One router per collector is what has been tested. Several exporters are accepted, but the live lane talks to one router.
