# Sample capture

`routeros7-ipfix-sample.bin` is a capture of real IPFIX export from a MikroTik router (RouterOS 7.20, L009), used by the tests.
It is **anonymised**: counters, sizes, ports and timing are original, every address is not.

| Original | In the sample |
|---|---|
| Public IPv4 addresses | One address each from `198.18.0.0/15` (the benchmarking range) |
| The WAN address (carrier-grade NAT) | An address in `100.64.0.0/10` |
| LAN and remote-site networks | `192.168.88.0/24` and `192.168.99.0/24`, host parts kept |
| IPv6 addresses | `fe80::1:N` for link-local, `2001:db8:P::/64` for everything else |
| MAC addresses | Manufacturer prefix kept, device part replaced |
| TCP sequence and acknowledgement numbers | Zeroed |

What the capture contains: 431 UDP datagrams, 4,289 flow records (template 258 for IPv4, template 259 for IPv6), about 270 seconds of export. The first datagram carries only the two templates.

## File format

Repeated until end of file:

| Bytes | Type | Meaning |
|---|---|---|
| 8 | float64, little-endian | Unix time the datagram was received by the collector host |
| 4 | uint32, little-endian | Payload length N |
| N | raw | UDP payload: one IPFIX message exactly as the router sent it |

Replay it into a running collector with:

```sh
./.devdata/nfp replay testdata/captures/routeros7-ipfix-sample.bin 127.0.0.1:2055
```

## Ground truth

`routeros7-ipfix-sample.groundtruth.json` records what was happening during the capture:

- `enabled` / `disabled`: the router's WAN and tunnel interface byte counters at the start and end of the export window.
- `transfers`: three transfers made from `192.168.88.234` with known local ports and exact payload sizes: a fast 50 MB download, a 90 MB download throttled to about 75 seconds, and a 15 MB upload.
- `samples`: counters and router CPU load roughly every 15 seconds.

Decoding the capture must give flow bytes of about payload × 1.04 (IP and TCP headers) for each transfer, and flow totals within about 1% of the interface counters.

## Contributing a capture

Captures from other RouterOS versions and hardware are welcome, but a raw capture is a record of what your network was doing. Anonymise it first:

```sh
scripts/anonymize-capture.py my-capture.bin my-capture-anon.bin \
    --map-net 192.168.1.0/24=192.168.88.0/24
```

`--map-net` moves a private network to another range, keeping host parts; repeat it for each network you want moved. The mapping depends on a random key that is discarded, so it cannot be recomputed from the output. Review the result before publishing it, and never commit the original.
