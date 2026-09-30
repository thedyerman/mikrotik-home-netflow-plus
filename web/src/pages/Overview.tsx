import { useMemo, useState, type ReactNode } from "react";
import { query, type Overview as OverviewData } from "../lib/api";
import { bytes, count, fmtBytes, fmtRate, pct, rate } from "../lib/format";
import { navigate, useFetch, useTween } from "../lib/hooks";
import { useLive } from "../lib/live";
import { useView } from "../lib/view";
import { Card, ErrorNote, Legend, PageHead, RangeControl, ZoneControl } from "../components/Controls";
import { IconChart, IconTable } from "../components/Icons";
import { RankList, type RankItem } from "../components/RankList";
import { ThroughputChart, ThroughputTable, type Sample } from "../components/ThroughputChart";

const LIVE_WINDOW = 300; // seconds of history on the live chart

function Kpi({ label, value, unit, sub, hero = false }: { label: string; value: ReactNode; unit?: string; sub?: ReactNode; hero?: boolean }) {
  return (
    <div className={"kpi" + (hero ? " hero" : "")}>
      <div className="kpi-label">{label}</div>
      <div className="kpi-value">{value}{unit && <span className="kpi-unit">{unit}</span>}</div>
      {sub && <div className="kpi-sub">{sub}</div>}
    </div>
  );
}

function LiveRate({ bps }: { bps: number }) {
  const r = fmtRate(useTween(bps));
  return <>{r.value}<span className="kpi-unit">{r.unit}</span></>;
}

export function Overview() {
  const { range, zone } = useView();
  const { tick, series } = useLive();
  const isLive = range === "live";
  const [table, setTable] = useState(false);
  const ov = useFetch<OverviewData>(isLive ? null : `/api/v1/overview${query({ range, zone })}`, range === "15m" || range === "1h" ? 10_000 : 60_000);

  const liveSeries = useMemo<Sample[]>(() => {
    const cutoff = (series.length ? series[series.length - 1][0] : 0) - LIVE_WINDOW;
    return series.filter((s) => s[0] >= cutoff);
  }, [series]);

  const data = ov.data;
  const chartSeries: Sample[] = isLive ? liveSeries : data?.series ?? [];
  const toRank = (items: { id: number; label: string; kind?: string; tunnel?: boolean; down: number; up: number; conns: number }[] | null | undefined): RankItem[] => items ?? [];

  const openFlows = (param: "dest" | "service", it: RankItem) =>
    navigate("/flows", { tab: "history", [param]: it.id, label: it.label });

  const rankNote = !isLive ? "by volume" : tick?.mode === "flows" ? "by rate over the last 90 s" : "by current rate";
  let chartNote: string;
  if (isLive) {
    chartNote = tick?.mode === "api" ? "WAN interface, one sample per second" : "From flow records; the most recent minute is still filling in";
  } else if (data?.source === "wan") {
    chartNote = `WAN interface counters, ${data.step < 60 ? data.step + " s" : data.step / 60 + " min"} averages`;
  } else {
    chartNote = data ? `From flow records, ${data.step < 3600 ? data.step / 60 + " min" : data.step / 3600 + " h"} averages` : "";
  }

  return (
    <div className="page">
      <PageHead title="Overview" sub={isLive ? "What the network is doing right now" : "Traffic over the selected period"}>
        {!isLive && <ZoneControl />}
        <RangeControl live />
      </PageHead>

      {ov.error && !isLive && <ErrorNote message={ov.error} />}

      <div className={"kpi-row" + (ov.stale ? " stale" : "")}>
        {isLive ? (
          <>
            <Kpi hero label="Download" value={<LiveRate bps={tick?.down ?? 0} />} sub={tick?.mode === "flows" ? "average over the last 90 s of flow records" : "right now"} />
            <Kpi label="Upload" value={<LiveRate bps={tick?.up ?? 0} />} />
            <Kpi label="Connections" value={count(tick?.conns ?? 0)} sub={`${count(tick?.active ?? 0)} moving data`} />
            <Kpi label="Devices" value={count(tick?.devices ?? 0)} sub="active in the last 5 min" />
            <Kpi label="Flow coverage" value={tick?.coverage != null ? pct(Math.min(tick.coverage, 1)) : "—"}
              sub={tick?.coverage != null ? "of WAN bytes seen in flow records" : tick?.apiConfigured ? "measuring…" : "needs the router API"} />
          </>
        ) : (
          <>
            <Kpi hero label="Downloaded" value={data ? fmtBytes(data.down).value : "—"} unit={data ? fmtBytes(data.down).unit : undefined} />
            <Kpi label="Uploaded" value={data ? fmtBytes(data.up).value : "—"} unit={data ? fmtBytes(data.up).unit : undefined} />
            <Kpi label="Peak download" value={data ? fmtRate(data.peakDown).value : "—"} unit={data ? fmtRate(data.peakDown).unit : undefined} />
            <Kpi label="Peak upload" value={data ? fmtRate(data.peakUp).value : "—"} unit={data ? fmtRate(data.peakUp).unit : undefined} />
            <Kpi label="Connections" value={data ? count(data.conns) : "—"} />
          </>
        )}
      </div>

      <Card title="Throughput" note={chartNote} stale={ov.stale}
        actions={
          <>
            <Legend />
            <button type="button" className="icon-btn" onClick={() => setTable(!table)} aria-pressed={table}
              title={table ? "Show chart" : "Show as table"} aria-label={table ? "Show chart" : "Show as table"}>
              {table ? <IconChart /> : <IconTable />}
            </button>
          </>
        }>
        {table ? <ThroughputTable series={chartSeries} /> : <ThroughputChart series={chartSeries} />}
      </Card>

      <div className={"grid-3" + (ov.stale ? " stale" : "")}>
        <Card title="Top devices" note={rankNote}>
          <RankList unit={isLive ? "rate" : "bytes"} items={toRank(isLive ? tick?.topDevices : data?.topDevices)}
            onSelect={(it) => it.id && navigate(`/devices/${it.id}`)} empty={isLive ? "No traffic right now" : "No traffic in this period"} />
        </Card>
        <Card title="Top destinations" note={rankNote}>
          <RankList unit={isLive ? "rate" : "bytes"} items={toRank(isLive ? tick?.topDests : data?.topDests)}
            onSelect={(it) => openFlows("dest", it)} empty={isLive ? "No traffic right now" : "No traffic in this period"} />
        </Card>
        <Card title="Top services" note={rankNote}>
          <RankList unit={isLive ? "rate" : "bytes"} items={toRank(isLive ? tick?.topServices : data?.topServices)}
            onSelect={(it) => openFlows("service", it)} empty={isLive ? "No traffic right now" : "No traffic in this period"} />
        </Card>
      </div>

      {!isLive && data && (
        <p className="foot-note">
          {bytes(data.down + data.up)} in total, averaging {rate(((data.down + data.up) * 8) / Math.max(1, data.to - data.from))}.
        </p>
      )}
    </div>
  );
}
