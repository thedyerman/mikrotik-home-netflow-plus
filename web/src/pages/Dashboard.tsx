import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import "../dashboard.css";
import type { RateItem, Tick, Today } from "../lib/api";
import { count, fmtBytes, fmtRate, pct } from "../lib/format";
import { useElementSize, useFetch, useNow, useTween } from "../lib/hooks";
import { LiveProvider, useLive } from "../lib/live";
import { ViewProvider } from "../lib/view";
import { IconCheck, IconCritical, IconWarn } from "../components/Icons";
import { ThroughputChart, type Sample } from "../components/ThroughputChart";

// The wall display: an unattended "now" view for a small always-on screen.
//
//   /dashboard                         fills whatever screen it is on
//   /dashboard?resolution=800x480      lays out for that size and scales to fit (also 1024x768, 1920x1080, 1080p, 720p)
//   /dashboard?rotate=true             one large panel that cycles, instead of everything at once
//   /dashboard?interval=20             seconds per panel when rotating (default 12)

interface Options {
  forced: { w: number; h: number } | null;
  rotate: boolean;
  interval: number;
}

function readOptions(): Options {
  const q = new URLSearchParams(window.location.search);
  // "resultion" is accepted too: it is an easy slip, and a silently ignored parameter is confusing on a kiosk.
  const raw = (q.get("resolution") ?? q.get("res") ?? q.get("resultion") ?? "").toLowerCase().trim();
  const named: Record<string, [number, number]> = { "1080p": [1920, 1080], "720p": [1280, 720], "480p": [800, 480] };
  let forced: Options["forced"] = null;
  const m = /^(\d{3,4})\s*[x×]\s*(\d{3,4})$/.exec(raw);
  if (m) forced = { w: Number(m[1]), h: Number(m[2]) };
  else if (named[raw]) forced = { w: named[raw][0], h: named[raw][1] };
  const flag = (q.get("rotate") ?? "").toLowerCase();
  const interval = Number(q.get("interval"));
  return {
    forced,
    rotate: flag === "true" || flag === "1" || flag === "yes",
    interval: Number.isFinite(interval) && interval >= 4 ? Math.min(interval, 600) : 12,
  };
}

function useWindowSize() {
  const [size, setSize] = useState({ w: window.innerWidth, h: window.innerHeight });
  useEffect(() => {
    const on = () => setSize({ w: window.innerWidth, h: window.innerHeight });
    window.addEventListener("resize", on);
    return () => window.removeEventListener("resize", on);
  }, []);
  return size;
}

export function Dashboard() {
  const options = useMemo(readOptions, []);
  return (
    <ViewProvider forceTheme="dark">
      <LiveProvider>
        <Stage options={options} />
      </LiveProvider>
    </ViewProvider>
  );
}

function Stage({ options }: { options: Options }) {
  const win = useWindowSize();
  const stage = options.forced ?? win;
  const scale = options.forced ? Math.min(win.w / stage.w, win.h / stage.h) : 1;
  // One unit is 1/48 of the height (10 px on an 800x480 panel), capped by the
  // width so narrow screens do not overflow sideways. Everything is sized in
  // these units, which is what lets one layout serve 480p up to 1080p.
  const unit = Math.min(stage.h / 48, stage.w / 80);
  return (
    <div className="dash-viewport">
      <div className="dash" style={{
        width: stage.w, height: stage.h, fontSize: unit,
        transform: scale === 1 ? undefined : `scale(${scale})`,
        left: (win.w - stage.w * scale) / 2, top: (win.h - stage.h * scale) / 2,
      }}>
        <Board options={options} unit={unit} wide={stage.w >= 1280} />
      </div>
    </div>
  );
}

function Rate({ bps, className }: { bps: number; className: string }) {
  const r = fmtRate(useTween(bps, 450));
  return <div className={className}>{r.value}<span className="dash-unit">{r.unit}</span></div>;
}

function Board({ options, unit, wide }: { options: Options; unit: number; wide: boolean }) {
  const { tick, connected, series } = useLive();
  const now = useNow(1000);

  // After an upgrade the server reports a different build: reload to pick it up.
  const build = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!tick?.build) return;
    if (build.current && build.current !== tick.build) window.location.reload();
    build.current = tick.build;
  }, [tick?.build]);

  // "Today" runs from local midnight; the key changes at midnight so the query restarts.
  const midnight = useMemo(() => {
    const d = new Date(now);
    d.setHours(0, 0, 0, 0);
    return Math.floor(d.getTime() / 1000);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [new Date(now).toDateString()]);
  const today = useFetch<Today>(`/api/v1/today?since=${midnight}`, 30_000).data;

  const windowSeconds = wide ? 600 : 300;
  const chart = useMemo<Sample[]>(() => {
    const cutoff = (series.length ? series[series.length - 1][0] : 0) - windowSeconds;
    return series.filter((s) => s[0] >= cutoff);
  }, [series, windowSeconds]);

  // Rotation: devices -> destinations -> today.
  const panels = ["devices", "dests", "today"] as const;
  const [panel, setPanel] = useState(0);
  useEffect(() => {
    if (!options.rotate) return;
    const t = window.setInterval(() => setPanel((p) => (p + 1) % panels.length), options.interval * 1000);
    return () => window.clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [options.rotate, options.interval]);

  const devices = tick?.topDevices ?? [];
  const dests = tick?.topDests ?? [];

  return (
    <div className={"dash-grid" + (options.rotate ? " rotate" : "")}>
      <header className="dash-head">
        <div className="dash-now">
          <div className="dash-label"><span className="dash-key down" />Download</div>
          <Rate className="dash-hero" bps={tick?.down ?? 0} />
        </div>
        <div className="dash-now">
          <div className="dash-label"><span className="dash-key up" />Upload</div>
          <Rate className={options.rotate ? "dash-hero second" : "dash-big"} bps={tick?.up ?? 0} />
        </div>
        {!options.rotate && <TodayCompact today={today} />}
      </header>

      <section className="dash-panel dash-chart">
        <div className="dash-panel-head">
          <span className="dash-title">Throughput</span>
          <span className="dash-note">last {windowSeconds / 60} minutes{tick?.mode === "flows" ? ", delayed" : ""}</span>
        </div>
        <div className="dash-chart-body">
          <ThroughputChart series={chart} fill display fontSize={Math.max(9, Math.round(unit * 1.15))} lineWidth={Math.max(2, unit * 0.2)} />
        </div>
      </section>

      {options.rotate ? (
        <section className="dash-panel dash-rotor" key={panels[panel]}>
          {panels[panel] === "devices" && <RankPanel title="Top devices" note="by current rate" items={devices} unit={unit} dots={[panel, panels.length]} />}
          {panels[panel] === "dests" && <RankPanel title="Top destinations" note="by current rate" items={dests} unit={unit} dots={[panel, panels.length]} />}
          {panels[panel] === "today" && <TodayLarge today={today} tick={tick} dots={[panel, panels.length]} />}
        </section>
      ) : (
        <div className="dash-lists">
          <section className="dash-panel"><RankPanel title="Top devices" items={devices} unit={unit} /></section>
          <section className="dash-panel"><RankPanel title="Top destinations" items={dests} unit={unit} /></section>
        </div>
      )}

      <StatusBar tick={tick} connected={connected} now={now} />
    </div>
  );
}

function Dots({ dots }: { dots?: [number, number] }) {
  if (!dots) return null;
  return (
    <span className="dash-dots" aria-hidden="true">
      {Array.from({ length: dots[1] }, (_, i) => <span key={i} className={i === dots[0] ? "on" : ""} />)}
    </span>
  );
}

function RankPanel({ title, note, items, unit, dots }: { title: string; note?: string; items: RateItem[]; unit: number; dots?: [number, number] }) {
  const [ref, size] = useElementSize<HTMLOListElement>();
  const minRow = unit * (dots ? 3.3 : 2.9); // the rotating panel uses larger type
  const rows = Math.max(1, Math.floor((size.height + 1) / minRow));
  const rowHeight = size.height / rows; // share out any leftover space
  const shown = items.slice(0, rows);
  const max = Math.max(1, ...shown.map((i) => i.down + i.up));
  return (
    <>
      <div className="dash-panel-head">
        <span className="dash-title">{title}</span>
        {note && <span className="dash-note">{note}</span>}
        <Dots dots={dots} />
      </div>
      <ol className="dash-rank" ref={ref}>
        {size.height > 0 && shown.map((it) => {
          const total = it.down + it.up;
          const r = fmtRate(total);
          return (
            <li key={it.id + it.label} style={{ height: rowHeight }}>
              <span className="dash-rank-name">{it.label || "unknown"}</span>
              <span className="dash-rank-track">
                <span className="dash-rank-bar" style={{ width: Math.max(2, (total / max) * 100) + "%" }}>
                  {it.down > 0 && <span className="down" style={{ flexBasis: (it.down / total) * 100 + "%" }} />}
                  {it.up > 0 && <span className="up" style={{ flexBasis: (it.up / total) * 100 + "%" }} />}
                </span>
              </span>
              <span className="dash-rank-val">{r.value}<span className="dash-unit">{r.unit}</span></span>
            </li>
          );
        })}
        {size.height > 0 && shown.length === 0 && <li className="dash-empty">Quiet right now</li>}
      </ol>
    </>
  );
}

function Figure({ label, value, unit, mark }: { label: string; value: string; unit?: string; mark?: "down" | "up" }) {
  return (
    <div className="dash-figure">
      <div className="dash-label">{mark && <span className={"dash-key " + mark} />}{label}</div>
      <div className="dash-figure-value">{value}{unit && <span className="dash-unit">{unit}</span>}</div>
    </div>
  );
}

function TodayCompact({ today }: { today?: Today }) {
  const d = today ? fmtBytes(today.down) : null, u = today ? fmtBytes(today.up) : null, p = today ? fmtRate(today.peakDown) : null;
  return (
    <div className="dash-today">
      <div className="dash-label">Today</div>
      <dl>
        <div><dt><span className="dash-key down" />Downloaded</dt><dd>{d ? <>{d.value}<span className="dash-unit">{d.unit}</span></> : "—"}</dd></div>
        <div><dt><span className="dash-key up" />Uploaded</dt><dd>{u ? <>{u.value}<span className="dash-unit">{u.unit}</span></> : "—"}</dd></div>
        <div><dt><span className="dash-key none" />Peak</dt><dd>{p ? <>{p.value}<span className="dash-unit">{p.unit}</span></> : "—"}</dd></div>
      </dl>
    </div>
  );
}

function TodayLarge({ today, tick, dots }: { today?: Today; tick: Tick | null; dots: [number, number] }) {
  const d = fmtBytes(today?.down ?? 0), u = fmtBytes(today?.up ?? 0), p = fmtRate(today?.peakDown ?? 0);
  return (
    <>
      <div className="dash-panel-head">
        <span className="dash-title">Today</span>
        <span className="dash-note">since midnight</span>
        <Dots dots={dots} />
      </div>
      <div className="dash-figures">
        <Figure mark="down" label="Downloaded" value={today ? d.value : "—"} unit={today ? d.unit : undefined} />
        <Figure mark="up" label="Uploaded" value={today ? u.value : "—"} unit={today ? u.unit : undefined} />
        <Figure label="Peak download" value={today ? p.value : "—"} unit={today ? p.unit : undefined} />
        <Figure label="Connections" value={today ? count(today.conns) : "—"} />
        <Figure label="Devices active" value={tick ? String(tick.devices) : "—"} />
        <Figure label="Flow coverage" value={tick?.coverage != null ? pct(Math.min(tick.coverage, 1)) : "—"} />
      </div>
    </>
  );
}

// The bottom strip is normally a quiet summary. It becomes a banner when
// something needs attention, most serious cause first.
function StatusBar({ tick, connected, now }: { tick: Tick | null; connected: boolean; now: number }) {
  // Measured on this screen's own clock: a Pi's clock need not agree with the server's.
  const lastTick = useRef(Date.now());
  useEffect(() => {
    if (tick) lastTick.current = Date.now();
  }, [tick]);
  const silent = Math.max(0, Math.round((now - lastTick.current) / 1000));

  let level: "ok" | "warn" | "bad" = "ok";
  let text: ReactNode = "Live";
  if (!connected || !tick) {
    if (silent > 6 || !tick) {
      level = "bad";
      text = tick ? `Collector unreachable for ${silent}s, reconnecting` : "Connecting to the collector";
    }
  } else if (tick.exportAge < 0) {
    level = "bad";
    text = "No flow records received from the router yet";
  } else if (tick.exportAge > 180) {
    level = "bad";
    text = `No flow records from the router for ${Math.round(tick.exportAge / 60)} min`;
  } else if (tick.alertTitle) {
    level = tick.alertSeverity === "critical" ? "bad" : "warn";
    text = tick.firing > 1 ? `${tick.alertTitle} (+${tick.firing - 1} more)` : tick.alertTitle;
  } else if (tick.mode === "flows") {
    level = tick.apiConfigured ? "warn" : "ok";
    text = tick.apiConfigured ? "Router API unreachable, rates are about a minute behind" : "Delayed: flow records only";
  }

  const clock = new Date(now).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });
  return (
    <footer className={"dash-status " + level}>
      <span className="dash-state">
        {level === "ok" ? <span className="dash-live-dot" /> : level === "warn" ? <IconWarn /> : <IconCritical />}
        {text}
      </span>
      {tick && level !== "bad" && (
        <span className="dash-counts">
          <span><b>{count(tick.conns)}</b> connections</span>
          <span><b>{tick.devices}</b> devices</span>
          {tick.coverage != null && <span><IconCheck /><b>{pct(Math.min(tick.coverage, 1))}</b> coverage</span>}
        </span>
      )}
      <span className="dash-clock">{tick?.routerName ? <span className="dash-router">{tick.routerName}</span> : null}{clock}</span>
    </footer>
  );
}
