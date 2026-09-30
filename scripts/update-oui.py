#!/usr/bin/env python3
"""Rebuild internal/enrich/data/oui.tsv.gz from the IEEE MA-L registry.

Usage: python3 scripts/update-oui.py
Output format: one "PREFIX<TAB>Vendor" line per assignment, gzip-compressed. PREFIX is
6, 7 or 9 hex digits (24-, 28- or 36-bit blocks); the longest match wins at lookup.
"""
import csv, gzip, io, os, re, urllib.request

# MA-L (24-bit), MA-M (28-bit) and MA-S (36-bit) registries.
URLS = ["https://standards-oui.ieee.org/oui/oui.csv", "https://standards-oui.ieee.org/oui28/mam.csv", "https://standards-oui.ieee.org/oui36/oui36.csv"]
OUT = os.path.join(os.path.dirname(__file__), "..", "internal", "enrich", "data", "oui.tsv.gz")
SUFFIX = re.compile(
    r"[,.]?\s*\b(inc|incorporated|corp|corporation|co|company|ltd|limited|llc|gmbh|ag|sa|s\.a|bv|b\.v|ab|oy|as|a/s|kg|plc|pte|pty|srl|s\.r\.l|spa|s\.p\.a|nv|"
    r"technologies|technology|electronics|international|communications|communication|systems|holdings|group|industrial|industries|"
    r"intl|mfg|\(shenzhen\)|\(shanghai\)|\(china\)|\(hk\)|\(usa\)|\(europe\))\.?$", re.I)

def clean(name):
    name = " ".join(name.replace("\t", " ").split()).strip(' ,."')
    for _ in range(4):
        short = SUFFIX.sub("", name).strip(" ,.&")
        if short == name or len(short) < 3:
            break
        name = short
    if name.isupper() and len(name) > 5:
        name = name.title()
    return name[:40]

rows = {}
for url in URLS:
    req = urllib.request.Request(url, headers={"User-Agent": "mikrotik-home-netflow-plus oui updater"})
    raw = urllib.request.urlopen(req, timeout=60).read().decode("utf-8", "replace")
    for r in csv.DictReader(io.StringIO(raw)):
        prefix = r["Assignment"].strip().upper()
        name = clean(r["Organization Name"])
        # A 24-bit block that is only a container for smaller assignments says nothing about the device.
        if len(prefix) in (6, 7, 9) and name and name != "IEEE Registration Authority":
            rows[prefix] = name
data = "".join(f"{p}\t{v}\n" for p, v in sorted(rows.items())).encode()
with gzip.GzipFile(OUT, "wb", mtime=0) as f:
    f.write(data)
print(f"{len(rows)} prefixes, {len(data)} bytes raw, {os.path.getsize(OUT)} bytes gzipped")
