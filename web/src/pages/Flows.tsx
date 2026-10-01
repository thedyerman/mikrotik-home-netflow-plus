import { Fragment, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { UnitHold, stableOrder, useFlip } from "../lib/liveui";
import { query, type FlowRow, type LiveRow } from "../lib/api";
import { bytes, clockShort, dayTime, duration, endpoint, rate } from "../lib/format";
import { href, navigate, useFetch, useRoute } from "../lib/hooks";
import { historical, useView } from "../lib/view";
import { Card, Empty, ErrorNote, PageHead, RangeControl, Segmented, ZoneControl } from "../components/Controls";
import { IconChevron, IconLock, IconPause, IconPlay, IconSearch, IconX } from "../components/Icons";

type GroupBy = "conn" | "device" | "dest" | "service";
type SortKey = "rate" | "down" | "up" | "total";

interface LiveResponse { rows: LiveRow[]; mode: "api" | "flows"; total: number; ts: number }

function Remote({ name, label, kind, ip, port }: { name: string; label: string; kind: string; ip: string; port: number }) {
  const primary = name || (kind !== "ip" ? label : "");
  return (
    <span className="two-line">
      {primary ? <span className="primary" title={primary}>{primary}</span> : null}
      <span className={"mono" + (primary ? " dim" : "")}>{endpoint(ip, port)}</span>
    </span>
  );
}

function ServiceCell({ service, tunnel, proto }: { service: string; tunnel: boolean; proto: string }) {
  return (
    <span className="svc">
      {tunnel && <IconLock className="inline-icon" aria-label="Tunnel" />}
      {service}
      <span className="tag">{proto}</span>
    </span>
  );
}

function ZoneTag({ zone, site }: { zone: string; site?: string }) {
  if (zone === "wan") return null;
  return <span className="tag">{zone === "site" ? site || "site" : "local"}</span>;
}

/** A rate with a thin bar behind it, scaled to the largest rate on screen. */
function RateCell({ value, max, kind, units, id }: { value: number; max: number; kind: "down" | "up"; units?: UnitHold; id?: string }) {
  const text = () => {
    if (value <= 0) return <span className="dim">—</span>;
    if (!units || !id) return rate(value);
    const r = units.format(id, value);
    return `${r.value} ${r.unit}`;
  };
  return (
    <span className="rate-cell">
      <span className={"mini-bar " + kind} style={{ width: (max > 0 ? Math.max(value > 0 ? 2 : 0, Math.min(100, (value / max) * 100)) : 0) + "%" }} />
      <span className="rate-text">{text()}</span>
    </span>
  );
}

const rowKey = (r: LiveRow) => `${r.proto}|${r.localIp}:${r.localPort}|${r.remoteIp}:${r.remotePort}`;

function matches(r: LiveRow, q: string): boolean {
  if (!q) return true;
  const hay = `${r.devName} ${r.localIp} ${r.remote} ${r.destLabel} ${r.remoteIp} ${r.service} ${r.proto} ${r.remotePort} ${r.localPort} ${r.zone} ${r.site ?? ""}`.toLowerCase();
  return q.toLowerCase().split(/\s+/).every((word) => hay.includes(word));
}

/** The live table. `compact` (used on the device page) starts with the fastest few rows. */
function LiveFlows({ device, compact = false }: { device?: number; compact?: boolean }) {
  const [expanded, setExpanded] = useState(!compact);
  const [showLocal, setShowLocal] = useState(false);
  const [paused, setPaused] = useState(false);
  const [q, setQ] = useState("");
  const [group, setGroup] = useState<GroupBy>("conn");
  const [sort, setSort] = useState<SortKey>("rate");
  const [open, setOpen] = useState<Set<string>>(new Set());
  const res = useFetch<LiveResponse>(paused ? null : "/api/v1/live/flows?limit=800", 1500);
  // While paused the last snapshot stays on screen.
  const held = useRef<LiveResponse | undefined>(undefined);
  if (res.data) held.current = res.data;
  const data = held.current;

  // Order is sticky: a row overtakes its neighbour only when clearly ahead,
  // so the table stops shuffling on every poll. Sorting by transferred bytes
  // is monotonic and needs no such help.
  const order = useRef<string[]>([]);
  const pending = useRef(new Map<string, number>());
  const groupOrder = useRef<string[]>([]);
  const groupPending = useRef(new Map<string, number>());
  const units = useRef(new UnitHold()).current;
  const scale = useRef({ down: 0, up: 0, at: 0 });
  const rows = useMemo(() => {
    const value = (r: { down: number; up: number; bytesDown: number; bytesUp: number }) =>
      sort === "down" ? r.down : sort === "up" ? r.up : sort === "total" ? r.bytesDown + r.bytesUp : r.down + r.up;
    const filtered = (data?.rows ?? []).filter((r) => (device === undefined || r.dev === device) && (showLocal || r.zone !== "local") && matches(r, q));
    if (sort === "total") {
      filtered.sort((a, b) => value(b) - value(a));
    } else {
      const byKey = new Map(filtered.map((r) => [rowKey(r), r]));
      order.current = stableOrder(order.current, new Map(filtered.map((r) => [rowKey(r), value(r)])), 1.2, 20_000, 2, pending.current);
      filtered.splice(0, filtered.length, ...order.current.map((k) => byKey.get(k)!));
    }
    return { list: filtered, value };
  }, [data, q, sort, device, showLocal]);
  const hiddenLocal = showLocal ? 0 : (data?.rows ?? []).filter((r) => r.zone === "local" && (device === undefined || r.dev === device)).length;

  const groups = useMemo(() => {
    if (group === "conn") return [];
    const m = new Map<string, { key: string; label: string; sub: string; rows: LiveRow[]; down: number; up: number; bytesDown: number; bytesUp: number }>();
    for (const r of rows.list) {
      const key = group === "device" ? "d" + r.dev + r.devName : group === "dest" ? "t" + r.dest : "s" + r.service;
      const label = group === "device" ? r.devName : group === "dest" ? r.destLabel : r.service;
      let g = m.get(key);
      if (!g) {
        g = { key, label, sub: group === "device" ? r.localIp : group === "dest" ? r.destKind : "", rows: [], down: 0, up: 0, bytesDown: 0, bytesUp: 0 };
        m.set(key, g);
      }
      g.rows.push(r);
      g.down += r.down; g.up += r.up; g.bytesDown += r.bytesDown; g.bytesUp += r.bytesUp;
    }
    const list = [...m.values()];
    if (sort === "total") return list.sort((a, b) => rows.value(b) - rows.value(a));
    const byKey = new Map(list.map((g) => [g.key, g]));
    groupOrder.current = stableOrder(groupOrder.current, new Map(list.map((g) => [g.key, rows.value(g)])), 1.2, 20_000, 2, groupPending.current);
    return groupOrder.current.map((k) => byKey.get(k)!);
  }, [rows, group, sort]);

  // Bar scale holds its peak and decays over ~10 s instead of rescaling every poll.
  const nowSec = performance.now() / 1000;
  const decay = Math.exp(-(nowSec - scale.current.at) / 10);
  const curDown = Math.max(0, ...(group === "conn" ? rows.list : groups).map((r) => r.down));
  const curUp = Math.max(0, ...(group === "conn" ? rows.list : groups).map((r) => r.up));
  if (curDown >= scale.current.down * decay || curUp >= scale.current.up * decay) {
    scale.current = { down: Math.max(curDown, scale.current.down * decay), up: Math.max(curUp, scale.current.up * decay), at: nowSec };
  }
  const maxDown = Math.max(curDown, scale.current.down * decay);
  const maxUp = Math.max(curUp, scale.current.up * decay);
  const tbodyRef = useRef<HTMLTableSectionElement>(null);
  useFlip(tbodyRef, rows.list);
  const th = (key: SortKey, label: string) => (
    <th className="num">
      <button type="button" className={"th-sort" + (sort === key ? " on" : "")} onClick={() => setSort(key)} aria-pressed={sort === key}>{label}</button>
    </th>
  );
  const connRow = (r: LiveRow, nested = false) => (
    <tr key={rowKey(r) + (nested ? "-n" : "")} data-flip={nested ? undefined : rowKey(r)} className={nested ? "nested" : undefined}>
      <td>
        {r.dev ? <a className="link" href={href(`/devices/${r.dev}`)}>{r.devName}</a> : r.devName}
        <span className="mono dim block">{endpoint(r.localIp, r.localPort)}</span>
      </td>
      <td><Remote name={r.remote} label={r.destLabel} kind={r.destKind} ip={r.remoteIp} port={r.remotePort} /></td>
      <td><ServiceCell service={r.service} tunnel={r.tunnel} proto={r.proto} /> <ZoneTag zone={r.zone} site={r.site} /></td>
      <td className="num"><RateCell value={r.down} max={maxDown} kind="down" units={units} id={rowKey(r) + "d"} /></td>
      <td className="num"><RateCell value={r.up} max={maxUp} kind="up" units={units} id={rowKey(r) + "u"} /></td>
      <td className="num">{bytes(r.bytesDown + r.bytesUp)}</td>
    </tr>
  );

  const shown = group === "conn" ? rows.list.slice(0, expanded ? 300 : 10) : [];
  return (
    <>
      <div className="filter-row">
        <label className="search">
          <IconSearch />
          <input className="input" type="search" placeholder="Filter by device, destination, address, port or service" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Filter connections" />
        </label>
        <Segmented<GroupBy> label="Group by" value={group} onChange={setGroup}
          options={[{ value: "conn", label: "Connections" }, { value: "device", label: "By device" }, { value: "dest", label: "By destination" }, { value: "service", label: "By service" }]} />
        <label className="check" title="Traffic to the router itself (DNS lookups, management) and local broadcast">
          <input type="checkbox" checked={showLocal} onChange={(e) => setShowLocal(e.target.checked)} />
          Local{hiddenLocal > 0 ? ` (${hiddenLocal})` : ""}
        </label>
        <button type="button" className="btn" onClick={() => setPaused(!paused)} aria-pressed={paused}>
          {paused ? <><IconPlay /> Resume</> : <><IconPause /> Pause</>}
        </button>
      </div>
      {res.error && <ErrorNote message={res.error} />}
      <Card className="flush">
        <div className="tbl-wrap">
          <table className="tbl">
            <thead>
              <tr>
                <th>{group === "conn" || group === "device" ? "Device" : group === "dest" ? "Destination" : "Service"}</th>
                <th>{group === "conn" ? "Remote" : "Connections"}</th>
                <th>{group === "conn" ? "Service" : ""}</th>
                {th("down", "Download")}
                {th("up", "Upload")}
                {th("total", "Transferred")}
              </tr>
            </thead>
            <tbody ref={tbodyRef}>
              {group === "conn" && shown.map((r) => connRow(r))}
              {group !== "conn" && groups.map((g) => {
                const isOpen = open.has(g.key);
                return (
                  <Fragment key={g.key}>
                    <tr className="group-row" data-flip={"g:" + g.key} onClick={() => { const n = new Set(open); isOpen ? n.delete(g.key) : n.add(g.key); setOpen(n); }}>
                      <td>
                        <button type="button" className={"caret" + (isOpen ? " open" : "")} aria-expanded={isOpen} aria-label={isOpen ? "Collapse" : "Expand"}><IconChevron /></button>
                        <span className="primary">{g.label}</span>
                      </td>
                      <td>{g.rows.length}</td>
                      <td className="dim">{g.sub}</td>
                      <td className="num"><RateCell value={g.down} max={maxDown} kind="down" units={units} id={"g:" + g.key + "d"} /></td>
                      <td className="num"><RateCell value={g.up} max={maxUp} kind="up" units={units} id={"g:" + g.key + "u"} /></td>
                      <td className="num">{bytes(g.bytesDown + g.bytesUp)}</td>
                    </tr>
                    {isOpen && g.rows.slice(0, 50).map((r) => connRow(r, true))}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
          {data && rows.list.length === 0 && <Empty>{q ? "No active connection matches the filter." : "No connection is moving data right now."}</Empty>}
        </div>
      </Card>
      <p className="foot-note">
        {data ? (
          <>
            {rows.list.length} active connection{rows.list.length === 1 ? "" : "s"}
            {rows.list.length > shown.length && group === "conn" ? `, fastest ${shown.length} shown` : ""}.{" "}
            {compact && group === "conn" && rows.list.length > 10 && (
              <button type="button" className="link-btn" onClick={() => setExpanded(!expanded)}>{expanded ? "Show fewer" : "Show all"}</button>
            )}{" "}
            {data.mode === "api" ? "Rates come from the router's connection table and update every 2 seconds." : "Rates come from flow records and are 15–75 seconds behind."}
            {paused ? " Paused." : ""}
          </>
        ) : "Loading…"}
      </p>
    </>
  );
}

function Chip({ children, onClear }: { children: ReactNode; onClear: () => void }) {
  return (
    <span className="chip">
      {children}
      <button type="button" onClick={onClear} aria-label="Remove filter"><IconX /></button>
    </span>
  );
}

function History() {
  const route = useRoute();
  const { range, zone } = useView();
  const r = historical(range);
  const p = route.params;
  const [text, setText] = useState(p.get("q") ?? "");
  const [q, setQ] = useState(text);
  const [sort, setSort] = useState<"bytes" | "time">("bytes");
  useEffect(() => {
    const t = window.setTimeout(() => setQ(text.trim()), 300);
    return () => window.clearTimeout(t);
  }, [text]);

  const filters = { device: p.get("device") ?? undefined, dest: p.get("dest") ?? undefined, service: p.get("service") ?? undefined };
  const label = p.get("label") ?? "";
  const res = useFetch<{ rows: FlowRow[]; foldBelow: number }>(`/api/v1/flows${query({ range: r, zone, ...filters, q, sort, limit: 300 })}`, 30_000);
  const clear = (key: "device" | "dest" | "service") =>
    navigate("/flows", { tab: "history", ...filters, [key]: undefined, label: undefined });
  const rows = res.data?.rows ?? [];
  const longRange = r === "7d" || r === "30d";

  return (
    <>
      <div className="filter-row">
        <label className="search">
          <IconSearch />
          <input className="input" type="search" placeholder="Search destination name or address" value={text} onChange={(e) => setText(e.target.value)} aria-label="Search connections" />
        </label>
        {filters.device && <Chip onClear={() => clear("device")}>Device: {label || "#" + filters.device}</Chip>}
        {filters.dest && <Chip onClear={() => clear("dest")}>Destination: {label || "#" + filters.dest}</Chip>}
        {filters.service && <Chip onClear={() => clear("service")}>Service: {label || "#" + filters.service}</Chip>}
        <span className="spacer" />
        <Segmented<"bytes" | "time"> label="Sort" value={sort} onChange={setSort} options={[{ value: "bytes", label: "Largest" }, { value: "time", label: "Most recent" }]} />
      </div>
      {res.error && <ErrorNote message={res.error} />}
      <Card className="flush" stale={res.stale}>
        <div className="tbl-wrap">
          <table className="tbl">
            <thead>
              <tr><th>Device</th><th>Remote</th><th>Service</th><th className="num">Download</th><th className="num">Upload</th><th className="num">Last active</th><th className="num">Duration</th></tr>
            </thead>
            <tbody>
              {rows.map((f) => (
                <tr key={f.id}>
                  <td>
                    <a className="link" href={href(`/devices/${f.dev}`)}>{f.devName}</a>
                    <span className="mono dim block">{endpoint(f.localIp, f.localPort)}</span>
                  </td>
                  <td><Remote name={f.remote} label={f.destLabel} kind={f.destKind} ip={f.remoteIp} port={f.remotePort} /></td>
                  <td><ServiceCell service={f.service} tunnel={f.tunnel} proto={f.proto} /> <ZoneTag zone={f.zone} /></td>
                  <td className="num">{bytes(f.down)}</td>
                  <td className="num">{bytes(f.up)}</td>
                  <td className="num">{longRange ? dayTime(f.last) : clockShort(f.last)}</td>
                  <td className="num">{duration(f.last - f.first)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {res.data && rows.length === 0 && <Empty>No stored connection matches. Individual connections are kept for 48 hours.</Empty>}
        </div>
      </Card>
      <p className="foot-note">
        {rows.length >= 300 ? "Showing the first 300 matches. " : ""}
        Connections under {bytes(res.data?.foldBelow ?? 10_000)} are counted in totals but not listed one by one.
      </p>
    </>
  );
}

export function Flows() {
  const route = useRoute();
  const tab = route.params.get("tab") === "history" ? "history" : "live";
  return (
    <div className="page">
      <PageHead title="Flows" sub={tab === "live" ? "Connections moving data right now" : "Stored connections from flow records"}>
        {tab === "history" && <ZoneControl />}
        {tab === "history" && <RangeControl />}
        <Segmented<"live" | "history"> label="View" value={tab} onChange={(t) => navigate("/flows", t === "history" ? { tab: "history" } : undefined)}
          options={[{ value: "live", label: <><span className="live-dot" />Live</> }, { value: "history", label: "History" }]} />
      </PageHead>
      {tab === "live" ? <LiveFlows /> : <History />}
    </div>
  );
}

export { LiveFlows };
