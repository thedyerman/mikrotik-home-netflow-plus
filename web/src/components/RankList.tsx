import { useRef } from "react";
import { bytes, rate } from "../lib/format";
import { UnitHold, useFlip, useLiveRanking } from "../lib/liveui";
import { IconLock } from "./Icons";
import { TipBody, tip } from "./Tip";

export interface RankItem { id: number; label: string; kind?: string; tunnel?: boolean; down: number; up: number; conns?: number }

// A ranking as thin horizontal bars: each bar is the item's download and
// upload laid end to end, so the length is the total and the split is visible.
//
// In live mode (unit "rate") the list is kept calm: values are smoothed a
// little more on top of what the server sends, rows only swap when clearly
// ahead, the bar scale decays slowly from its peak, each row keeps its unit
// until the value is well past the boundary, and reorders slide into place.
export function RankList({ items, unit, onSelect, empty = "Nothing yet", limit }: {
  items: RankItem[];
  unit: "rate" | "bytes";
  onSelect?: (item: RankItem) => void;
  empty?: string;
  /** Rows to show; the server sends extra candidates so that the order can be kept stable. */
  limit?: number;
}) {
  const live = unit === "rate";
  const ranked = useLiveRanking(items, live ? { attack: 1, release: 2, hold: 10 } : { attack: 0, release: 0, hold: 0, ratio: 1, margin: 0 });
  const all = live ? ranked.items : items;
  const shown = limit ? all.slice(0, limit) : all;
  const max = live ? ranked.max : Math.max(1, ...items.map((i) => i.down + i.up));
  const units = useRef(new UnitHold()).current;
  const listRef = useRef<HTMLOListElement>(null);
  useFlip(listRef, shown);

  if (shown.length === 0) return <div className="empty small">{empty}</div>;
  const fmt = (key: string, v: number) => {
    if (!live) return bytes(v);
    const r = units.format(key, v);
    return `${r.value} ${r.unit}`;
  };
  units.forget(new Set(shown.map((it) => `${it.id}:${it.label}`)));
  return (
    <ol className={"rank" + (live ? " live" : "")} ref={listRef}>
      {shown.map((it) => {
        const key = `${it.id}:${it.label}`;
        const total = it.down + it.up;
        const width = Math.max(1.5, Math.min(100, (total / max) * 100));
        const downShare = total > 0 ? (it.down / total) * 100 : 0;
        const tipProps = tip(() => (
          <TipBody title={it.label} rows={[
            { key: "down", label: "Download", value: live ? rate(it.down) : bytes(it.down) },
            { key: "up", label: "Upload", value: live ? rate(it.up) : bytes(it.up) },
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
                <span className="rank-seg down" style={{ flexBasis: downShare + "%", display: it.down > 0 ? undefined : "none" }} />
                <span className="rank-seg up" style={{ flexBasis: 100 - downShare + "%", display: it.up > 0 ? undefined : "none" }} />
              </span>
            </span>
            <span className="rank-val">{fmt(key, total)}</span>
          </>
        );
        return (
          <li key={key} data-flip={key}>
            {onSelect
              ? <button type="button" className="rank-row click" onClick={() => onSelect(it)} {...tipProps}>{body}</button>
              : <div className="rank-row" tabIndex={0} {...tipProps}>{body}</div>}
          </li>
        );
      })}
    </ol>
  );
}
