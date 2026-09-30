# Setting up the MikroTik router

This guide configures a RouterOS 7 router to feed mikrotik-home-netflow-plus. It has four parts:

1. [Send flow records to the collector](#1-send-flow-records-to-the-collector) (required)
2. [Create a read-only API user](#2-create-a-read-only-api-user) (for live rates and names)
3. [Give the API a certificate](#3-give-the-api-a-certificate) (so that session is encrypted)
4. [Keep the router clock right](#4-keep-the-router-clock-right) (recommended)

followed by [how to check it worked](#check-that-it-works), [troubleshooting](#troubleshooting) and [how to undo everything](#undoing-the-setup).

Step 1 alone gives you history, rankings and alerts. Steps 2 and 3 add second-by-second rates, device names and DNS names.

## Before you start

You need:

- **RouterOS 7.** The commands were tested on 7.20. Check with `/system resource print`.
- **A terminal on the router** with full rights: WinBox → New Terminal, WebFig → Terminal, or SSH.
- **The collector's IP address**, and it must not change. Give the collector host a static address or a static DHCP lease (`/ip dhcp-server lease make-static`).
- **The router's LAN address**, the one the collector will talk to.

The examples use these values. Replace them with your own:

| Placeholder | Example | Meaning |
|---|---|---|
| `<COLLECTOR_IP>` | `192.168.88.10` | The host running the collector |
| `<ROUTER_LAN_IP>` | `192.168.88.1` | The router's address on the LAN |
| `<STRONG_PASSWORD>` | | A long random password for the API user |

Once the collector is running, its **Status** page shows this whole script with your addresses already filled in.

### What the router can and cannot report

Traffic Flow sees every packet the router's CPU forwards, including FastTracked connections. It does not see:

- traffic between two devices on the same LAN segment, which is switched in hardware and never routed;
- traffic handled by layer-3 hardware offload, on models that have it.

You do **not** need to disable FastTrack.

## 1. Send flow records to the collector

```
/ip traffic-flow set enabled=yes active-flow-timeout=1m
/ip traffic-flow target add dst-address=<COLLECTOR_IP> port=2055 version=ipfix \
    src-address=<ROUTER_LAN_IP> v9-template-refresh=20 v9-template-timeout=1m
```

What each setting does:

| Setting | Why |
|---|---|
| `enabled=yes` | Turns Traffic Flow on |
| `active-flow-timeout=1m` | How often a still-active flow is reported. One minute is the lowest value RouterOS accepts; the default of 30 minutes would make long downloads invisible for half an hour. The collector's `NFP_ACTIVE_FLOW_TIMEOUT` must match (it defaults to `1m`) |
| `version=ipfix` | The only export format that carries NAT translation fields, MAC addresses and IPv6 |
| `src-address` | The address export packets come from. The collector only accepts packets from addresses listed in `NFP_EXPORTERS`, so make this the address you configured there |
| `v9-template-refresh=20`, `v9-template-timeout=1m` | How often the router repeats the record layout. The collector needs it once after a cold start; these values keep that wait short |

Leave the rest at its defaults:

- `interfaces=all`. The collector needs both directions and sorts out what is what.
- `inactive-flow-timeout=15s`.
- `cache-entries`. The default is enough for a home network. Raise it (for example `cache-entries=16k`) if the collector's coverage figure stays low on a busy network.

### IPFIX fields

RouterOS exports every field by default, which is what the collector expects. If you have trimmed the list under `/ip traffic-flow ipfix`, make sure these are still on:

```
/ip traffic-flow ipfix set bytes=yes packets=yes protocol=yes \
    src-address=yes dst-address=yes src-port=yes dst-port=yes \
    in-interface=yes out-interface=yes src-mac-address=yes \
    first-forwarded=yes last-forwarded=yes sys-init-time=yes \
    nat-src-address=yes nat-dst-address=yes nat-src-port=yes nat-dst-port=yes
```

`nat-events` can stay off. It produces separate event records the collector does not use.

## 2. Create a read-only API user

```
/user group add name=flowmon policy=read,api
/user add name=flowmon group=flowmon password=<STRONG_PASSWORD> address=<COLLECTOR_IP>/32
```

- The `flowmon` group can read and can use the API. It cannot write, cannot change policy, cannot log in over SSH, WinBox or the web, and cannot read sensitive values.
- `address=<COLLECTOR_IP>/32` means the account only works from the collector.

The collector uses this account to read the connection table, interface counters, DHCP leases, the ARP table, the DNS cache, addresses and routes. Put the user name and password in the collector's configuration as `NFP_ROUTER_USER` and `NFP_ROUTER_PASSWORD`.

Never give the collector an administrative account. It does not need one.

## 3. Give the API a certificate

The collector talks to the encrypted API service, `api-ssl`, on port 8729. Out of the box that service has no certificate and only offers anonymous cipher suites, which the collector cannot use. A self-signed certificate is enough:

```
/certificate add name=api-cert common-name=router.lan key-size=2048 days-valid=3650 \
    key-usage=key-cert-sign,crl-sign,digital-signature,key-encipherment,tls-server
/certificate sign api-cert
/ip service set api-ssl certificate=api-cert
```

Signing takes a few seconds. Wait for it to finish before running the last line.

The `key-cert-sign` usage matters: without it RouterOS refuses to self-sign and reports `CA not found`.

The collector remembers the certificate it sees on its first connection and refuses any other afterwards (the fingerprint is stored as `router-cert.pin` in its data volume and shown on the Status page). To pin a certificate yourself, copy its fingerprint from `/certificate print detail` into `NFP_ROUTER_CERT_FINGERPRINT`.

### Make sure the service is reachable

```
/ip service print where name~"api"
```

`api-ssl` must be enabled, and if its `address` column restricts who may connect, that list must include the collector:

```
/ip service set api-ssl disabled=no address=<COLLECTOR_IP>/32
```

If your firewall's input chain is restrictive, allow TCP 8729 from the collector.

### Without TLS

If you would rather not manage a certificate, the plain API on port 8728 works too: set `NFP_ROUTER_TLS=false` on the collector. The password then crosses your LAN unencrypted, so keep the account read-only and address-restricted as above.

## 4. Keep the router clock right

```
/system ntp client set enabled=yes servers=time.cloudflare.com
```

The collector corrects for a wrong router clock by itself, so this is not required for it to work. It is still worth doing: certificates, logs and schedules on the router all depend on the time.

## Check that it works

On the router:

```
/ip traffic-flow print
/ip traffic-flow target print
/user print detail where name=flowmon
/ip service print where name=api-ssl
```

You should see Traffic Flow enabled with a one-minute active timeout, one target pointing at the collector, the `flowmon` user restricted to the collector's address, and `api-ssl` with `api-cert`.

On the collector's **Status** page:

| Card | What you want to see |
|---|---|
| Flow export | "Last export just now", records being decoded, no records lost |
| Router API | "Connected", with the router's model and RouterOS version |
| Flow coverage | Above 95% after about five minutes of traffic |
| Network | Your LAN under local networks and the right interface under WAN interfaces |

The indicator at the bottom of the sidebar says **Live** when both lanes work, and **Delayed** when only flow records are arriving.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| "No flow records received yet" | Check the target's `dst-address` and port. Confirm nothing on the collector host blocks UDP 2055. With Docker, use host networking or publish `2055/udp` |
| Packets "rejected from other sources" | The router sends from a different address than the one in `NFP_EXPORTERS`. Set `src-address` on the target to the router's LAN address |
| `failure: CA not found` when signing | The certificate lacks the `key-cert-sign` usage. Remove it (`/certificate remove api-cert`) and create it again with the command above |
| "TLS handshake failed" | `api-ssl` has no certificate assigned, or signing had not finished when you assigned it. Run `/ip service set api-ssl certificate=api-cert` again |
| "connection refused" | `api-ssl` is disabled, or its `address` list excludes the collector |
| "invalid user name or password" | Wrong password, or the user's `address` does not match the address the collector connects from |
| "certificate changed" | You replaced the router's certificate. Delete `router-cert.pin` from the collector's data volume and restart it |
| "not enough permissions" on the Status page | The group is missing `read` or `api`. Check `/user group print where name=flowmon` |
| Coverage well below 95% | Export packets are being dropped on the way, the flow cache is full (raise `cache-entries`), or hardware offload is handling part of the traffic |
| Long downloads appear late | `active-flow-timeout` is still at its 30-minute default |

## Undoing the setup

```
/ip traffic-flow set enabled=no active-flow-timeout=30m
/ip traffic-flow target remove [find dst-address=<COLLECTOR_IP>]
/user remove flowmon
/user group remove flowmon
/ip service set api-ssl certificate=none
/certificate remove api-cert
```

Each line reverses one step above; run only the ones you need.

## Security notes

- The API account can read the router's configuration, including the connection table and DHCP leases. Keep its password out of version control and restrict it by address.
- Flow export is unencrypted UDP on your LAN. It contains addresses, ports and byte counts, not packet contents.
- Nothing in this guide opens a port to the internet.
