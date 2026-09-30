#!/usr/bin/env python3
"""Anonymise an IPFIX capture so it can be shared.

The capture format is the one written by the collector's test tooling: repeated
records of a little-endian float64 receive time, a little-endian uint32 length
and the UDP payload (see testdata/captures/README.md).

Flow records are rewritten in place, so sizes, counters and timing are
untouched. What changes:

  IPv4   public addresses      -> 198.18.0.0/15 (benchmarking range), one new address each
         100.64.0.0/10 (CGNAT) -> other addresses inside 100.64.0.0/10
         networks given with --map-net are moved, keeping the host part
         other private, loopback, multicast and broadcast addresses are kept
  IPv6   link-local            -> fe80::1:N
         everything else       -> 2001:db8:P::/64 (documentation range), one P per original /64
         solicited-node groups -> ff02::1:ff00:N (they embed part of a MAC address)
         other multicast, loopback and unspecified are kept
  MAC    the manufacturer prefix is kept, the device part is replaced
  TCP    sequence and acknowledgement numbers are zeroed

The replacement values depend on a random key that is thrown away, so the
mapping cannot be recomputed from the output.

Usage:
  scripts/anonymize-capture.py IN.bin OUT.bin [--map-net OLD=NEW ...]
                               [--json IN.json OUT.json] [--mapping FILE]

--json rewrites address strings in a companion JSON file with the same mapping.
--mapping writes the old -> new table; keep that file private.
"""
import argparse, hashlib, ipaddress, json, os, struct, sys

IPV4_FIELDS = {8, 12, 15, 225, 226}
IPV6_FIELDS = {27, 28, 62, 281, 282}
MAC_FIELDS = {56, 57, 80, 81}
ZERO_FIELDS = {184, 185}  # TCP sequence / acknowledgement numbers

CGNAT = ipaddress.ip_network("100.64.0.0/10")
KEEP4 = [ipaddress.ip_network(n) for n in
         ("0.0.0.0/8", "10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/3")]
SOLICITED = ipaddress.ip_network("ff02::1:ff00:0/104")


class Mapper:
    def __init__(self, netmaps):
        self.key = os.urandom(32)
        self.netmaps = netmaps
        self.v4, self.v6, self.mac = {}, {}, {}
        self.public = self.cgnat = self.linklocal = self.solicited = 0
        self.prefixes, self.hosts = {}, {}
        self.used_macs = set()

    def ipv4(self, raw: bytes) -> bytes:
        if raw in self.v4:
            return self.v4[raw]
        ip = ipaddress.IPv4Address(raw)
        new = ip
        for old_net, new_net in self.netmaps:
            if ip in old_net:
                new = ipaddress.IPv4Address(int(new_net.network_address) + int(ip) - int(old_net.network_address))
                break
        else:
            if ip in CGNAT:
                self.cgnat += 1
                new = ipaddress.IPv4Address(int(CGNAT.network_address) + 0x100 + self.cgnat)
            elif not any(ip in n for n in KEEP4):
                self.public += 1
                new = ipaddress.IPv4Address(int(ipaddress.IPv4Address("198.18.0.0")) + 0x100 + self.public)
        self.v4[raw] = new.packed
        return new.packed

    def ipv6(self, raw: bytes) -> bytes:
        if raw in self.v6:
            return self.v6[raw]
        ip = ipaddress.IPv6Address(raw)
        new = ip
        if ip in SOLICITED:
            self.solicited += 1
            new = ipaddress.IPv6Address(int(SOLICITED.network_address) + self.solicited)
        elif ip.is_multicast or ip.is_unspecified or ip.is_loopback:
            pass
        elif ip.is_link_local:
            self.linklocal += 1
            new = ipaddress.IPv6Address((0xFE80 << 112) + (1 << 16) + self.linklocal)
        else:
            prefix, host = int(ip) >> 64, int(ip) & ((1 << 64) - 1)
            p = self.prefixes.setdefault(prefix, len(self.prefixes) + 1)
            if host > 0xFFFF:  # long interface identifiers are usually derived from a MAC
                host = (1 << 16) + self.hosts.setdefault((prefix, host), len(self.hosts) + 1)
            new = ipaddress.IPv6Address((0x20010DB8 << 96) + (p << 64) + host)
        self.v6[raw] = new.packed
        return new.packed

    def mac_addr(self, raw: bytes) -> bytes:
        if raw in self.mac:
            return self.mac[raw]
        new = raw
        if raw == b"\x33\x33\xff" + raw[3:]:  # IPv6 solicited-node group: the tail comes from a MAC
            new = raw[:3] + hashlib.sha256(self.key + raw).digest()[:3]
        elif raw not in (b"\x00" * 6, b"\xff" * 6) and not raw[0] & 1:
            n = 0
            while True:
                tail = hashlib.sha256(self.key + raw + bytes([n])).digest()[:3]
                if raw[:3] + tail not in self.used_macs:
                    break
                n += 1
            new = raw[:3] + tail
            self.used_macs.add(new)
        self.mac[raw] = new
        return new


def rewrite(payload: bytearray, templates: dict, m: Mapper, stats: dict):
    version, length, _, _, domain = struct.unpack("!HHIII", payload[:16])
    if version != 10:
        sys.exit(f"not an IPFIX message (version {version})")
    off = 16
    while off + 4 <= min(length, len(payload)):
        set_id, set_len = struct.unpack("!HH", payload[off:off + 4])
        if set_len < 4:
            break
        end = off + set_len
        o = off + 4
        if set_id == 2:
            while o + 4 <= end:
                tid, count = struct.unpack("!HH", payload[o:o + 4])
                o += 4
                if tid < 256:
                    break
                fields = []
                for _ in range(count):
                    fid, flen = struct.unpack("!HH", payload[o:o + 4])
                    o += 4
                    ent = None
                    if fid & 0x8000:
                        ent, fid = struct.unpack("!I", payload[o:o + 4])[0], fid & 0x7FFF
                        o += 4
                    if flen == 0xFFFF:
                        sys.exit("variable-length fields are not supported")
                    fields.append((fid, flen, ent))
                templates[(domain, tid)] = fields
        elif set_id >= 256:
            fields = templates.get((domain, set_id))
            if fields is None:
                sys.exit(f"data set {set_id} arrived before its template; refusing to leave records untouched")
            size = sum(f[1] for f in fields)
            while o + size <= end:
                for fid, flen, ent in fields:
                    if ent is None:
                        if fid in IPV4_FIELDS and flen == 4:
                            payload[o:o + 4] = m.ipv4(bytes(payload[o:o + 4]))
                        elif fid in IPV6_FIELDS and flen == 16:
                            payload[o:o + 16] = m.ipv6(bytes(payload[o:o + 16]))
                        elif fid in MAC_FIELDS and flen == 6:
                            payload[o:o + 6] = m.mac_addr(bytes(payload[o:o + 6]))
                        elif fid in ZERO_FIELDS:
                            payload[o:o + flen] = bytes(flen)
                    o += flen
                stats["records"] += 1
        off = end


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("src")
    ap.add_argument("dst")
    ap.add_argument("--map-net", action="append", default=[], metavar="OLD=NEW")
    ap.add_argument("--json", nargs=2, metavar=("IN", "OUT"))
    ap.add_argument("--mapping", metavar="FILE")
    args = ap.parse_args()

    netmaps = []
    for spec in args.map_net:
        old, new = (ipaddress.ip_network(x) for x in spec.split("="))
        if old.prefixlen != new.prefixlen:
            sys.exit(f"--map-net {spec}: both networks must be the same size")
        netmaps.append((old, new))
    m = Mapper(netmaps)

    templates, stats, out = {}, {"records": 0}, []
    with open(args.src, "rb") as f:
        while hdr := f.read(12):
            ts, n = struct.unpack("<dI", hdr)
            payload = bytearray(f.read(n))
            rewrite(payload, templates, m, stats)
            out.append(struct.pack("<dI", ts, n) + payload)
    with open(args.dst, "wb") as f:
        f.write(b"".join(out))

    v4 = {str(ipaddress.IPv4Address(k)): str(ipaddress.IPv4Address(v)) for k, v in m.v4.items()}
    v6 = {str(ipaddress.IPv6Address(k)): str(ipaddress.IPv6Address(v)) for k, v in m.v6.items()}
    mac = {k.hex(":"): v.hex(":") for k, v in m.mac.items()}
    table = {**v4, **v6, **mac}

    if args.json:
        def walk(x):
            if isinstance(x, dict):
                return {k: walk(v) for k, v in x.items()}
            if isinstance(x, list):
                return [walk(v) for v in x]
            return table.get(x, x) if isinstance(x, str) else x
        with open(args.json[0]) as f:
            data = json.load(f)
        with open(args.json[1], "w") as f:
            json.dump(walk(data), f, indent=1)
            f.write("\n")
    if args.mapping:
        with open(args.mapping, "w") as f:
            json.dump({"ipv4": v4, "ipv6": v6, "mac": mac}, f, indent=1)

    changed = sum(1 for k, v in table.items() if k != v)
    print(f"{len(out)} datagrams, {stats['records']} records rewritten; "
          f"{changed} of {len(table)} distinct addresses replaced "
          f"({m.public} public IPv4, {m.cgnat} CGNAT, {len(m.prefixes)} IPv6 prefixes, {len(m.used_macs)} MACs)")


if __name__ == "__main__":
    main()
