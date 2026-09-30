import { useMemo, useState } from "react";
import { sankey, sankeyLinkHorizontal, type SankeyGraph, type SankeyLink as D3Link, type SankeyNode as D3Node } from "d3-sankey";
import type { SankeyData, SankeyNode } from "../lib/api";
import { bytes, pct } from "../lib/format";
import { useElementWidth } from "../lib/hooks";
import { TipBody, tip } from "./Tip";

type N = D3Node<SankeyNode, { down: number; up: number }>;
type L = D3Link<SankeyNode, { down: number; up: number }>;

const COLUMN_TITLES = ["Devices", "Services", "Destinations"];

// Devices -> services -> destinations. Ribbon thickness is volume; one hue is
// used throughout because thickness, not colour, carries the data.
export function Sankey({ data, onSelect }: { data: SankeyData; onSelect?: (n: SankeyNode) => void }) {
  const [ref, width] = useElementWidth<HTMLDivElement>();
  const [hover, setHover] = useState<string | null>(null);

  const layout = useMemo(() => {
    if (width < 200 || data.nodes.length === 0) return null;
    const narrow = width < 720;
    const left = narrow ? 8 : 170, right = narrow ? 8 : 190, top = 28;
    const perCol = [0, 1, 2].map((c) => data.nodes.filter((n) => n.col === c).length);
    const height = Math.max(380, Math.max(...perCol) * 40) + top;
    const graph: SankeyGraph<SankeyNode, { down: number; up: number }> = {
      nodes: data.nodes.map((n) => ({ ...n })),
      links: data.links.filter((l) => l.down + l.up > 0).map((l) => ({ source: l.source, target: l.target, value: l.down + l.up, down: l.down, up: l.up })),
    };
    const gen = sankey<SankeyNode, { down: number; up: number }>()
      .nodeId((n) => n.id)
      .nodeAlign((n) => n.col)
      .nodeWidth(10)
      .nodePadding(18)
      .nodeSort((a, b) => (b.value ?? 0) - (a.value ?? 0) || a.label.localeCompare(b.label))
      .extent([[left, top], [width - right, height - 8]]);
    const out = gen(graph);
    return { ...out, height, left, right, narrow };
  }, [data, width]);

  const path = sankeyLinkHorizontal<SankeyNode, { down: number; up: number }>();
  const connected = (l: L) => hover !== null && ((l.source as N).id === hover || (l.target as N).id === hover);

  return (
    <div ref={ref} className="sankey">
      {layout && (
        <svg width={width} height={layout.height} role="img" aria-label="Traffic from devices through services to destinations">
          {[0, 1, 2].map((c) => {
            const x = c === 0 ? layout.left : c === 2 ? width - layout.right : (layout.left + width - layout.right) / 2;
            return <text key={c} x={x + (c === 0 ? 10 : 0)} y={14} className="sankey-col" textAnchor={c === 0 ? "end" : c === 2 ? "start" : "middle"}>{COLUMN_TITLES[c]}</text>;
          })}
          <g fill="none">
            {layout.links.map((l, i) => {
              const s = l.source as N, t = l.target as N;
              return (
                <path key={i} d={path(l) ?? ""} strokeWidth={Math.max(1, l.width ?? 1)}
                  className={"sankey-link" + (hover === null ? "" : connected(l) ? " on" : " off")}
                  {...tip(() => (
                    <TipBody title={`${s.label} → ${t.label}`} rows={[
                      { key: "down", label: "Download", value: bytes(l.down) },
                      { key: "up", label: "Upload", value: bytes(l.up) },
                      { key: "none", label: "of all traffic shown", value: pct(data.total ? l.value / data.total : 0, 1) },
                    ]} />
                  ))} />
              );
            })}
          </g>
          {layout.nodes.map((n) => {
            const h = Math.max(2, (n.y1 ?? 0) - (n.y0 ?? 0));
            const mid = ((n.y0 ?? 0) + (n.y1 ?? 0)) / 2;
            const labelLeft = n.col === 0 && !layout.narrow;
            const tx = labelLeft ? (n.x0 ?? 0) - 8 : (n.x1 ?? 0) + 8;
            const maxChars = layout.narrow ? 14 : n.col === 1 ? 16 : 24;
            const label = n.label.length > maxChars ? n.label.slice(0, maxChars - 1) + "…" : n.label;
            const nodeTip = tip(() => (
              <TipBody title={n.label} rows={[
                { key: "none", label: "Total", value: bytes(n.value ?? 0) },
                { key: "none", label: "of all traffic shown", value: pct(data.total ? (n.value ?? 0) / data.total : 0, 1) },
              ]} />
            ));
            return (
              <g key={n.id} className={"sankey-node" + (n.kind === "other" ? " other" : "") + (onSelect && n.ref ? " click" : "")}
                tabIndex={0} role={onSelect && n.ref ? "button" : undefined}
                {...nodeTip}
                onPointerEnter={() => setHover(n.id)}
                onPointerLeave={() => { setHover(null); nodeTip.onPointerLeave(); }}
                onClick={() => n.ref && onSelect?.(n)}
                onKeyDown={(e) => { if (e.key === "Enter" && n.ref) onSelect?.(n); }}>
                {/* a generous invisible hit area around the thin node */}
                <rect x={(n.x0 ?? 0) - 6} y={(n.y0 ?? 0) - 2} width={22} height={h + 4} fill="transparent" />
                <rect x={n.x0} y={n.y0} width={(n.x1 ?? 0) - (n.x0 ?? 0)} height={h} rx={2} className="sankey-rect" />
                <text x={tx} y={h > 26 ? mid - 7 : mid} dy="0.35em" textAnchor={labelLeft ? "end" : "start"} className="sankey-label">{label}</text>
                {h > 26 && (
                  <text x={tx} y={mid + 8} dy="0.35em" textAnchor={labelLeft ? "end" : "start"} className="sankey-value">{bytes(n.value ?? 0)}</text>
                )}
              </g>
            );
          })}
        </svg>
      )}
    </div>
  );
}
