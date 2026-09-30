import { useState } from "react";
import { query, type AlertEvent, type DeviceDetail, type SankeyData } from "../lib/api";
import { ago, bytes, dayTime, fmtBytes, fmtRate } from "../lib/format";
import { href, navigate, useFetch, useNow } from "../lib/hooks";
import { historical, useView } from "../lib/view";
import { Card, Empty, ErrorNote, Legend, PageHead, RangeControl } from "../components/Controls";
import { IconChart, IconChevron, IconTable } from "../components/Icons";
import { RankList } from "../components/RankList";
import { RenameDevice } from "../components/Rename";
import { Sankey } from "../components/Sankey";
import { ThroughputChart, ThroughputTable } from "../components/ThroughputChart";
import { EventList } from "./Alerts";
import { LiveFlows } from "./Flows";

export function DevicePage({ id }: { id: number }) {
  const { range } = useView();
  const r = historical(range);
  const res = useFetch<DeviceDetail>(`/api/v1/devices/${id}${query({ range: r })}`, 10_000);
  const map = useFetch<SankeyData>(`/api/v1/sankey${query({ range: r, zone: "all", device: id })}`, 60_000);
  const events = useFetch<{ events: AlertEvent[] }>(`/api/v1/alerts/events${query({ device: id, limit: 20 })}`, 30_000);
  const [table, setTable] = useState(false);
  const now = useNow(5000);
  const d = res.data;

  if (res.error && !d) {
    return (
      <div className="page">
        <PageHead title="Device" />
        <ErrorNote message={res.error} />
        <a className="link" href={href("/devices")}>Back to devices</a>
      </div>
    );
  }
  const dev = d?.device;
  const zones = d?.zones ?? {};
  const zoneRows = [
    { key: "wan", label: "Internet" }, { key: "site", label: "Remote sites" }, { key: "local", label: "Router and local" },
  ].map((z) => ({ ...z, down: zones[z.key]?.[0] ?? 0, up: zones[z.key]?.[1] ?? 0 })).filter((z) => z.down + z.up > 0);

  return (
    <div className="page">
      <nav className="crumbs" aria-label="Breadcrumb">
        <a className="link" href={href("/devices")}>Devices</a><IconChevron /><span>{dev?.name ?? "…"}</span>
      </nav>
      <PageHead
        title={dev ? <RenameDevice id={dev.id} name={dev.name} customName={dev.customName} onSaved={res.reload} /> : "…"}
        sub={dev && (
          <span className="meta-line">
            {dev.ips.length > 0 && <span className="mono">{dev.ips.join(", ")}</span>}
            {dev.mac && <span className="mono">{dev.mac}</span>}
            {dev.vendor && <span>{dev.vendor}</span>}
            {dev.hostname && dev.hostname !== dev.name && <span>hostname {dev.hostname}</span>}
            <span>first seen {dayTime(dev.firstSeen)}</span>
            <span>last seen {ago(dev.lastSeen, now)}</span>
          </span>
        )}>
        <RangeControl />
      </PageHead>

      <div className={"kpi-row" + (res.stale ? " stale" : "")}>
        <div className="kpi hero"><div className="kpi-label">Download now</div><div className="kpi-value">{fmtRate(dev?.liveDown ?? 0).value}<span className="kpi-unit">{fmtRate(dev?.liveDown ?? 0).unit}</span></div></div>
        <div className="kpi"><div className="kpi-label">Upload now</div><div className="kpi-value">{fmtRate(dev?.liveUp ?? 0).value}<span className="kpi-unit">{fmtRate(dev?.liveUp ?? 0).unit}</span></div></div>
        <div className="kpi"><div className="kpi-label">Downloaded, {r}</div><div className="kpi-value">{fmtBytes(d?.down ?? 0).value}<span className="kpi-unit">{fmtBytes(d?.down ?? 0).unit}</span></div></div>
        <div className="kpi"><div className="kpi-label">Uploaded, {r}</div><div className="kpi-value">{fmtBytes(d?.up ?? 0).value}<span className="kpi-unit">{fmtBytes(d?.up ?? 0).unit}</span></div></div>
        <div className="kpi"><div className="kpi-label">Connections, {r}</div><div className="kpi-value">{(d?.conns ?? 0).toLocaleString("en-US")}</div><div className="kpi-sub">{dev?.liveConns ?? 0} moving data now</div></div>
      </div>

      <Card title="Throughput" note="From flow records, all traffic of this device" stale={res.stale}
        actions={
          <>
            <Legend />
            <button type="button" className="icon-btn" onClick={() => setTable(!table)} aria-pressed={table}
              title={table ? "Show chart" : "Show as table"} aria-label={table ? "Show chart" : "Show as table"}>
              {table ? <IconChart /> : <IconTable />}
            </button>
          </>
        }>
        {table ? <ThroughputTable series={d?.series ?? []} /> : <ThroughputChart series={d?.series ?? []} height={240} />}
      </Card>

      <div className={"grid-3" + (res.stale ? " stale" : "")}>
        <Card title="Top destinations" note="by volume">
          <RankList unit="bytes" items={d?.topDests ?? []} empty="No traffic in this period"
            onSelect={(it) => navigate("/flows", { tab: "history", device: id, dest: it.id, label: `${dev?.name} → ${it.label}` })} />
        </Card>
        <Card title="Top services" note="by volume">
          <RankList unit="bytes" items={d?.topServices ?? []} empty="No traffic in this period"
            onSelect={(it) => navigate("/flows", { tab: "history", device: id, service: it.id, label: `${dev?.name} · ${it.label}` })} />
        </Card>
        <Card title="Where it went" note="by zone">
          {zoneRows.length === 0 ? <div className="empty small">No traffic in this period</div> : (
            <table className="tbl compact">
              <thead><tr><th>Zone</th><th className="num">Download</th><th className="num">Upload</th></tr></thead>
              <tbody>
                {zoneRows.map((z) => <tr key={z.key}><td>{z.label}</td><td className="num">{bytes(z.down)}</td><td className="num">{bytes(z.up)}</td></tr>)}
              </tbody>
            </table>
          )}
        </Card>
      </div>

      <h2 className="section-title">Active connections</h2>
      <LiveFlows device={id} compact />

      <Card title="Flow map" note={`Services and destinations of this device, ${r}`} stale={map.stale}>
        {map.data && map.data.nodes.length > 0
          ? <Sankey data={map.data} onSelect={(n) => n.col > 0 && navigate("/flows", { tab: "history", device: id, [n.col === 1 ? "service" : "dest"]: n.ref, label: `${dev?.name} · ${n.label}` })} />
          : <Empty>No traffic in this period.</Empty>}
      </Card>

      <Card title="Alerts for this device">
        {events.data && events.data.events.length > 0 ? <EventList events={events.data.events} now={now} /> : <div className="empty small">No alerts for this device.</div>}
      </Card>
    </div>
  );
}
