import { bytes, rate } from "../lib/format";
import { IconLock } from "./Icons";
import { TipBody, tip } from "./Tip";

export interface RankItem { id: number; label: string; kind?: string; tunnel?: boolean; down: number; up: number; conns?: number }

// A ranking as thin horizontal bars: each bar is the item's download and
// upload laid end to end, so the length is the total and the split is visible.
export function RankList({ items, unit, onSelect, empty = "Nothing yet" }: {
  items: RankItem[];
  unit: "rate" | "bytes";
  onSelect?: (item: RankItem) => void;
  empty?: string;
}) {
  const fmt = unit === "rate" ? rate : bytes;
  const max = Math.max(1, ...items.map((i) => i.down + i.up));
  if (items.length === 0) return <div className="empty small">{empty}</div>;
  return (
    <ol className="rank">
      {items.map((it) => {
        const total = it.down + it.up;
        const width = Math.max(1.5, (total / max) * 100);
        const downShare = total > 0 ? (it.down / total) * 100 : 0;
        const tipProps = tip(() => (
          <TipBody title={it.label} rows={[
            { key: "down", label: "Download", value: fmt(it.down) },
            { key: "up", label: "Upload", value: fmt(it.up) },
            ...(it.conns !== undefined ? [{ key: "none" as const, label: "Connections", value: it.conns.toLocaleString("en-US") }] : []),
          ]} />
        ));
        const body = (
          <>
            <span className={"rank-label" + (it.kind === "ip" ? " mono" : "")} title={it.label}>
              {it.tunnel && <IconLock className="inline-icon" />}
              {it.label || "unknown"}
            </span>
            <span className="rank-track">
              <span className="rank-bar" style={{ width: width + "%" }}>
                {it.down > 0 && <span className="rank-seg down" style={{ flexBasis: downShare + "%" }} />}
                {it.up > 0 && <span className="rank-seg up" style={{ flexBasis: 100 - downShare + "%" }} />}
              </span>
            </span>
            <span className="rank-val">{fmt(total)}</span>
          </>
        );
        return (
          <li key={it.id + ":" + it.label}>
            {onSelect
              ? <button type="button" className="rank-row click" onClick={() => onSelect(it)} {...tipProps}>{body}</button>
              : <div className="rank-row" tabIndex={0} {...tipProps}>{body}</div>}
          </li>
        );
      })}
    </ol>
  );
}
