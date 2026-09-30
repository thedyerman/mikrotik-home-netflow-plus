import { useEffect, useMemo, useRef } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import { clock, clockShort, day, dayTime, rate } from "../lib/format";
import { useElementSize } from "../lib/hooks";
import { useView } from "../lib/view";

export type Sample = [ts: number, down: number, up: number];

function hexAlpha(hex: string, alpha: number): string {
  const h = hex.trim().replace("#", "");
  const n = parseInt(h.length === 3 ? h.split("").map((c) => c + c).join("") : h, 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
}

// Download is drawn above the baseline and upload mirrored below it, on one
// shared scale, so the two never occlude each other.
//
// `fill` sizes the chart to its container instead of a fixed height, and
// `display` is the wall-display variant: no hover layer, no time axis, and
// type sized by `fontSize`.
export function ThroughputChart({ series, height = 280, fill = false, display = false, fontSize = 11, lineWidth = 2 }: {
  series: Sample[]; height?: number; fill?: boolean; display?: boolean; fontSize?: number; lineWidth?: number;
}) {
  const [wrapRef, size] = useElementSize<HTMLDivElement>();
  const width = size.width;
  if (fill) height = size.height;
  const tipRef = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const { dark } = useView();

  const data = useMemo<uPlot.AlignedData>(() => {
    const xs = new Array<number>(series.length);
    const down = new Array<number>(series.length);
    const up = new Array<number>(series.length);
    for (let i = 0; i < series.length; i++) {
      xs[i] = series[i][0];
      down[i] = series[i][1];
      up[i] = -series[i][2];
    }
    return [xs, down, up];
  }, [series]);
  const latest = useRef(data);
  latest.current = data;

  useEffect(() => {
    const el = wrapRef.current;
    if (!el || width < 50 || height < 40) return;
    const css = getComputedStyle(el);
    const token = (name: string) => css.getPropertyValue(name).trim();
    const downColor = token("--down"), upColor = token("--up");
    const font = `${fontSize}px ${css.fontFamily}`;
    const tipEl = tipRef.current!;

    const spanOf = (u: uPlot) => (u.scales.x.max ?? 0) - (u.scales.x.min ?? 0);
    const xLabel = (u: uPlot, ts: number) => {
      const span = spanOf(u);
      if (span <= 1200) return clock(ts);
      if (span <= 2 * 86400) return clockShort(ts);
      return day(ts);
    };

    const opts: uPlot.Options = {
      width,
      height,
      padding: [12, 18, 0, 0],
      legend: { show: false },
      cursor: {
        show: !display,
        y: false,
        drag: { x: false, y: false },
        points: { size: 9, width: 2, stroke: () => token("--surface"), fill: (_u, i) => (i === 1 ? downColor : upColor) },
      },
      scales: {
        x: { time: true },
        y: {
          // Keep at least a fifth of the height for upload so it stays readable next to a large download.
          range: (_u, min, max) => {
            const top = Math.max(max, 1000);
            const bottom = Math.min(min, -top * 0.22);
            return [bottom * 1.12, top * 1.12];
          },
        },
      },
      axes: [
        {
          show: !display,
          stroke: token("--muted"), font, size: 30, gap: 6,
          grid: { show: false }, ticks: { show: false },
          values: (u, splits) => splits.map((s) => xLabel(u, s)),
          space: 90,
        },
        {
          stroke: token("--muted"), font, size: Math.round(fontSize * 5.9), gap: Math.round(fontSize * 0.7),
          grid: { stroke: token("--grid"), width: 1 }, ticks: { show: false },
          values: (_u, splits) => splits.map((v) => rate(Math.abs(v))),
          space: Math.round(fontSize * 4),
        },
      ],
      series: [
        {},
        { label: "Download", stroke: downColor, width: lineWidth, fill: hexAlpha(downColor, 0.12), points: { show: false } },
        { label: "Upload", stroke: upColor, width: lineWidth, fill: hexAlpha(upColor, 0.12), points: { show: false } },
      ],
      hooks: {
        draw: [
          (u) => { // the zero baseline
            const y = Math.round(u.valToPos(0, "y", true)) + 0.5;
            const { ctx, bbox } = u;
            ctx.save();
            ctx.strokeStyle = token("--axis");
            ctx.lineWidth = 1;
            ctx.beginPath();
            ctx.moveTo(bbox.left, y);
            ctx.lineTo(bbox.left + bbox.width, y);
            ctx.stroke();
            ctx.restore();
          },
        ],
        setCursor: [
          (u) => {
            const { idx, left } = u.cursor;
            if (display || idx == null || left == null || left < 0) {
              tipEl.style.opacity = "0";
              return;
            }
            const ts = u.data[0][idx];
            const span = spanOf(u);
            tipEl.querySelector<HTMLElement>("[data-t]")!.textContent = span <= 3600 ? clock(ts) : dayTime(ts);
            tipEl.querySelector<HTMLElement>("[data-d]")!.textContent = rate(u.data[1][idx] ?? 0);
            tipEl.querySelector<HTMLElement>("[data-u]")!.textContent = rate(Math.abs(u.data[2][idx] ?? 0));
            const x = left + u.over.offsetLeft;
            const w = tipEl.offsetWidth;
            tipEl.style.transform = `translate(${x + 14 + w > width ? x - w - 14 : x + 14}px, 12px)`;
            tipEl.style.opacity = "1";
          },
        ],
      },
    };
    const u = new uPlot(opts, latest.current, el);
    plot.current = u;
    return () => {
      u.destroy();
      plot.current = null;
    };
  }, [wrapRef, width, height, dark, display, fontSize, lineWidth]);

  useEffect(() => {
    plot.current?.setData(data);
  }, [data]);

  return (
    <div className="chart" ref={wrapRef} style={fill ? { height: "100%" } : { height }}>
      <div className="tip chart-tip" ref={tipRef} aria-hidden="true">
        <div className="tip-head" data-t />
        <div className="tip-row"><span className="key-line down" /><span className="tip-val" data-d /><span className="tip-label">Download</span></div>
        <div className="tip-row"><span className="key-line up" /><span className="tip-val" data-u /><span className="tip-label">Upload</span></div>
      </div>
    </div>
  );
}

/** The table twin of the chart: the same samples as rows. */
export function ThroughputTable({ series, maxRows = 120 }: { series: Sample[]; maxRows?: number }) {
  const stride = Math.max(1, Math.ceil(series.length / maxRows));
  const rows = series.filter((_, i) => i % stride === 0).reverse();
  const span = series.length ? series[series.length - 1][0] - series[0][0] : 0;
  return (
    <div className="tbl-wrap chart-table">
      <table className="tbl">
        <thead>
          <tr><th>Time</th><th className="num">Download</th><th className="num">Upload</th></tr>
        </thead>
        <tbody>
          {rows.map(([ts, d, u]) => (
            <tr key={ts}>
              <td className="mono">{span <= 3600 ? clock(ts) : dayTime(ts)}</td>
              <td className="num">{rate(d)}</td>
              <td className="num">{rate(u)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
