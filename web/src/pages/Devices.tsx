import { useMemo, useState } from "react";
import { query, type DeviceInfo } from "../lib/api";
import { ago, bytes, rate } from "../lib/format";
import { navigate, useFetch, useNow } from "../lib/hooks";
import { historical, useView } from "../lib/view";
import { Card, Empty, ErrorNote, PageHead, RangeControl } from "../components/Controls";
import { IconRouter, IconSearch } from "../components/Icons";
import { RenameDevice } from "../components/Rename";
import { Sparkline } from "../components/Sparkline";

type SortKey = "volume" | "live" | "name" | "seen";

export function Devices() {
  const { range } = useView();
  const r = historical(range);
  const res = useFetch<{ devices: DeviceInfo[] }>(`/api/v1/devices${query({ range: r })}`, 5000);
  const [sort, setSort] = useState<SortKey>("volume");
  const [q, setQ] = useState("");
  const now = useNow(5000);

  const devices = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const list = (res.data?.devices ?? []).filter((d) =>
      !needle || `${d.name} ${d.hostname} ${d.mac} ${d.vendor} ${d.ips.join(" ")}`.toLowerCase().includes(needle));
    const key = (d: DeviceInfo) => sort === "volume" ? d.down + d.up : sort === "live" ? d.liveDown + d.liveUp : sort === "seen" ? d.lastSeen : 0;
    return list.sort((a, b) => sort === "name" ? a.name.localeCompare(b.name) : key(b) - key(a) || a.name.localeCompare(b.name));
  }, [res.data, sort, q]);
  const maxVolume = Math.max(1, ...devices.map((d) => d.down + d.up));

  const th = (key: SortKey, label: string, num = true) => (
    <th className={num ? "num" : undefined}>
      <button type="button" className={"th-sort" + (sort === key ? " on" : "")} onClick={() => setSort(key)} aria-pressed={sort === key}>{label}</button>
    </th>
  );

  return (
    <div className="page">
      <PageHead title="Devices" sub="Everything on the local network that has sent traffic through the router">
        <RangeControl />
      </PageHead>
      <div className="filter-row">
        <label className="search">
          <IconSearch />
          <input className="input" type="search" placeholder="Filter by name, address, MAC or vendor" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Filter devices" />
        </label>
      </div>
      {res.error && <ErrorNote message={res.error} />}
      <Card className="flush" stale={res.stale}>
        <div className="tbl-wrap">
          <table className="tbl">
            <thead>
              <tr>
                {th("name", "Device", false)}
                <th>Address</th>
                {th("live", "Now")}
                {th("volume", `Volume, ${r}`)}
                <th>Trend</th>
                {th("seen", "Last seen")}
              </tr>
            </thead>
            <tbody>
              {devices.map((d) => {
                const total = d.down + d.up;
                return (
                  <tr key={d.id} className="row-click" onClick={() => navigate(`/devices/${d.id}`)}>
                    <td>
                      <span className="primary">
                        {d.router && <IconRouter className="inline-icon" />}
                        <RenameDevice id={d.id} name={d.name} customName={d.customName} onSaved={res.reload} />
                      </span>
                      <span className="dim block">{d.router ? "The router's own traffic" : d.vendor || "Unknown vendor"}</span>
                    </td>
                    <td>
                      <span className="mono">{d.ips[0] ?? "—"}</span>
                      <span className="mono dim block">{d.mac || " "}</span>
                    </td>
                    <td className="num">
                      {d.liveDown + d.liveUp > 0 ? (
                        <span className="two-line right">
                          <span><span className="key-dot down" />{rate(d.liveDown)}</span>
                          <span><span className="key-dot up" />{rate(d.liveUp)}</span>
                        </span>
                      ) : <span className="dim">idle</span>}
                    </td>
                    <td className="num">
                      <span className="vol">
                        <span className="vol-text">{bytes(total)}</span>
                        <span className="rank-track">
                          <span className="rank-bar" style={{ width: Math.max(total > 0 ? 1.5 : 0, (total / maxVolume) * 100) + "%" }}>
                            {d.down > 0 && <span className="rank-seg down" style={{ flexBasis: (d.down / total) * 100 + "%" }} />}
                            {d.up > 0 && <span className="rank-seg up" style={{ flexBasis: (d.up / total) * 100 + "%" }} />}
                          </span>
                        </span>
                      </span>
                    </td>
                    <td><Sparkline values={d.spark} /></td>
                    <td className="num dim">{ago(d.lastSeen, now)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {res.data && devices.length === 0 && <Empty>{q ? "No device matches the filter." : "No devices seen yet. They appear as soon as flow records arrive."}</Empty>}
        </div>
      </Card>
      <p className="foot-note">Volume counts traffic leaving the local network (internet and remote sites). Devices are identified by MAC address, so a changed IP address does not split a device in two.</p>
    </div>
  );
}
