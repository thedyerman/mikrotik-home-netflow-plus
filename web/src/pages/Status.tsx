import { useState, type ReactNode } from "react";
import type { Status } from "../lib/api";
import { ago, bytes, count, dayTime, duration, pct, rate } from "../lib/format";
import { useFetch, useNow } from "../lib/hooks";
import { Card, ErrorNote, PageHead } from "../components/Controls";
import { IconCheck, IconCopy, IconCritical, IconWarn } from "../components/Icons";

function KV({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="kv">
      {rows.map(([k, v]) => (
        <div key={k}><dt>{k}</dt><dd>{v}</dd></div>
      ))}
    </dl>
  );
}

function State({ level, children }: { level: "good" | "warn" | "bad" | "idle"; children: ReactNode }) {
  return (
    <span className={"state " + level}>
      {level === "good" ? <IconCheck /> : level === "warn" ? <IconWarn /> : level === "bad" ? <IconCritical /> : null}
      {children}
    </span>
  );
}

function setupScript(s: Status): string {
  const collector = s.setup.collector || "<COLLECTOR_IP>";
  const routerIp = s.udp.exporters?.[0];
  return [
    "# Flow export to this collector",
    "/ip traffic-flow set enabled=yes active-flow-timeout=1m",
    `/ip traffic-flow target add dst-address=${collector} port=${s.setup.flowPort} version=ipfix` +
      (routerIp ? ` src-address=${routerIp}` : "") + " v9-template-refresh=20 v9-template-timeout=1m",
    "",
    "# Read-only API user for live rates and device names",
    "/user group add name=flowmon policy=read,api",
    `/user add name=flowmon group=flowmon password=<STRONG_PASSWORD> address=${collector}/32`,
    "",
    "# Certificate so the API can be used over TLS (port 8729)",
    "/certificate add name=api-cert common-name=router.lan key-size=2048 days-valid=3650 key-usage=key-cert-sign,crl-sign,digital-signature,key-encipherment,tls-server",
    "/certificate sign api-cert",
    "/ip service set api-ssl certificate=api-cert",
  ].join("\n");
}

export function StatusPage() {
  const res = useFetch<Status>("/api/v1/status", 3000);
  const now = useNow(1000);
  const [copied, setCopied] = useState(false);
  const s = res.data;

  if (!s) {
    return <div className="page"><PageHead title="Status" />{res.error ? <ErrorNote message={res.error} /> : <div className="empty">Loading…</div>}</div>;
  }
  const e = s.engine;
  const exportLevel = e.exportAge < 0 ? "bad" : e.exportAge > 120 ? "warn" : "good";
  const cov = e.coverage;
  const named = e.naming.domain + e.naming.org + e.naming.ip;
  const script = setupScript(s);
  const copy = () => navigator.clipboard?.writeText(script).then(() => { setCopied(true); window.setTimeout(() => setCopied(false), 1500); });
  const clock = s.clocks?.[0];

  return (
    <div className="page">
      <PageHead title="Status" sub={`${s.app} ${s.version} · running for ${duration(s.now - e.started)}`} />
      {res.error && <ErrorNote message={res.error} />}

      <div className="grid-2">
        <div className="stack">
        <Card title="Flow export" note={`IPFIX over UDP on ${s.udp.listen}`}>
          <KV rows={[
            ["State", e.exportAge < 0
              ? <State level="bad">No flow records received yet</State>
              : <State level={exportLevel}>Last export {ago(e.lastExport, now)}</State>],
            ["Exporters allowed", s.udp.exporters?.length ? <span className="mono">{s.udp.exporters.join(", ")}</span> : "any source"],
            ["Packets", <>{count(s.udp.packets)} received{s.udp.rejected > 0 ? `, ${count(s.udp.rejected)} rejected from other sources` : ""}</>],
            ["Records", <>{count(s.decoder.records)} decoded, {s.decoder.templates} templates</>],
            ["Lost in transit", s.decoder.lostRecords === 0
              ? <State level="good">none detected</State>
              : <State level="warn">{count(s.decoder.lostRecords)} records ({pct(s.decoder.lostRecords / Math.max(1, s.decoder.records + s.decoder.lostRecords), 2)})</State>],
            ["Undecodable", s.decoder.errors + s.decoder.unknownTemplate === 0 ? "none" : `${s.decoder.errors} malformed, ${s.decoder.unknownTemplate} before their template arrived`],
            ["Router clock", clock ? <>{Math.abs(clock.offsetMs) < 1500 ? "in sync" : `${(Math.abs(clock.offsetMs) / 1000).toFixed(1)} s ${clock.offsetMs > 0 ? "behind" : "ahead"}`} (corrected automatically)</> : "—"],
            ["Router booted", clock ? <>{dayTime(Date.parse(clock.bootTime) / 1000)}{clock.reboots > 0 ? `, ${clock.reboots} reboot${clock.reboots > 1 ? "s" : ""} seen` : ""}</> : "—"],
          ]} />
        </Card>

        <Card title="Flow coverage" note="Flow-record bytes compared with the WAN interface counters">
          {cov.known ? (
            <>
              <div className="meter-head">
                <span className="meter-value">{pct(Math.min(cov.total, 1), 1)}</span>
                <State level={cov.total >= 0.95 ? "good" : cov.total >= 0.85 ? "warn" : "bad"}>
                  {cov.total >= 0.95 ? "Flow records account for nearly all traffic" : cov.total >= 0.85 ? "Some traffic is not in flow records" : "A large share of traffic is missing from flow records"}
                </State>
              </div>
              <div className="meter" role="img" aria-label={`Coverage ${pct(Math.min(cov.total, 1), 1)}`}><span style={{ width: Math.min(100, cov.total * 100) + "%" }} /></div>
              <KV rows={[
                ["Download", pct(Math.min(cov.down, 1.05), 1)],
                ["Upload", pct(Math.min(cov.up, 1.05), 1)],
                ["Window", `${duration(cov.seconds)}, ending 90 s ago`],
              ]} />
            </>
          ) : (
            <p className="prose">{e.apiEnabled ? "Not enough data yet. Coverage needs a few minutes of interface counters and at least a little traffic." : "Coverage needs the router API, which supplies the interface counters to compare against."}</p>
          )}
        </Card>

        <Card title="Network" note="How addresses are classified">
          <KV rows={[
            ["Local networks", <span className="mono wrap">{e.localNets.join(", ") || "—"}</span>],
            ["Remote sites", e.sites.length ? e.sites.map((x) => <span key={x.prefix} className="block"><span className="mono">{x.prefix}</span> via {x.name}</span>) : "none"],
            ["WAN interfaces", e.wan.length ? e.wan.join(", ") : e.apiEnabled ? "detecting…" : "unknown without the router API"],
            ["Router addresses", <span className="mono wrap">{e.routerAddrs.join(", ") || "—"}</span>],
          ]} />
          {e.interfaces.length > 0 && (
            <table className="tbl compact">
              <thead><tr><th>Interface</th><th>Type</th><th className="num">Receiving</th><th className="num">Sending</th></tr></thead>
              <tbody>
                {e.interfaces.filter((i) => i.running).map((i) => (
                  <tr key={i.name}>
                    <td>{i.name}{i.wan && <span className="tag">WAN</span>}</td>
                    <td className="dim">{i.type}</td>
                    <td className="num">{rate(i.rxBps)}</td>
                    <td className="num">{rate(i.txBps)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>

        </div>
        <div className="stack">
        <Card title="Router API" note={e.apiEnabled ? `${s.routerApi.addr}${s.routerApi.tls ? " over TLS" : " without TLS"}` : "Not configured"}>
          {e.apiEnabled ? (
            <KV rows={[
              ["State", e.apiUp ? <State level="good">Connected {ago(e.apiSince, now)}</State> : <State level="bad">Not connected</State>],
              ...(e.apiError ? [["Last error", <span className="error-inline">{e.apiError}</span>] as [string, ReactNode]] : []),
              ["Router", e.router ? `${e.router.identity || "MikroTik"} · ${e.router.board} · RouterOS ${e.router.version}` : "—"],
              ["Uptime", e.router?.uptime ?? "—"],
              ["CPU load", e.router ? `${e.router.cpuLoad}%` : "—"],
              ["Connections tracked", count(Math.max(0, e.trackedConns))],
              ["Certificate", s.routerApi.fingerprint ? <span className="mono wrap">{s.routerApi.fingerprint}</span> : s.routerApi.tls ? "not pinned yet" : "—"],
            ]} />
          ) : (
            <p className="prose">Running from flow records only, so live views are 15–75 seconds behind and devices are named by address. Set <code>NFP_ROUTER_ADDR</code>, <code>NFP_ROUTER_USER</code> and <code>NFP_ROUTER_PASSWORD</code> to add second-by-second rates, device names and DNS names.</p>
          )}
        </Card>

        <Card title="Names" note="How destinations of open internet connections are labelled">
          <KV rows={[
            ["By DNS name", named ? `${e.naming.domain} (${pct(e.naming.domain / named)})` : "—"],
            ["By organisation", named ? `${e.naming.org} (${pct(e.naming.org / named)})` : "—"],
            ["Bare address", named ? `${e.naming.ip} (${pct(e.naming.ip / named)})` : "—"],
            ["DNS names remembered", count(e.dnsNames)],
            ["Organisation database", e.asnDatabase ? `${e.asnDatabase}, built ${dayTime(e.asnBuilt)}` : "not installed"],
            ["Devices known", count(e.devices)],
          ]} />
        </Card>

        <Card title="Storage" note="SQLite database on the data volume">
          <div className="meter-head">
            <span className="meter-value">{bytes(s.store.bytes)}</span>
            <span className="dim">of {bytes(s.store.maxBytes)} allowed</span>
          </div>
          <div className="meter" role="img" aria-label="Storage used"><span style={{ width: Math.min(100, (s.store.bytes / s.store.maxBytes) * 100) + "%" }} /></div>
          <KV rows={[
            ["Connections", `${count(s.store.tables.conns ?? 0)} rows, kept ${s.store.retention.connections}`],
            ["Minute rollups", `${count(s.store.tables.rollup_1m ?? 0)} rows, kept ${s.store.retention.minute}`],
            ["Hourly rollups", `${count(s.store.tables.rollup_1h ?? 0)} rows, kept ${s.store.retention.hour}`],
            ["Folding", `connections under ${bytes(s.store.foldBelow)} are counted but not stored one by one`],
          ]} />
        </Card>
        </div>
      </div>

      <Card title="Router setup" note="Paste into a RouterOS terminal. Replace the password, then put the same user and password in the collector's configuration."
        actions={<button type="button" className="btn small" onClick={copy}>{copied ? <><IconCheck /> Copied</> : <><IconCopy /> Copy</>}</button>}>
        <pre className="code-block">{script}</pre>
      </Card>

      {s.attribution && <p className="foot-note">{s.attribution}</p>}
    </div>
  );
}
