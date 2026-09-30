import type { ReactNode } from "react";
import type { RangeName, ZoneName } from "../lib/api";
import { useView } from "../lib/view";

export function Segmented<T extends string>({ label, options, value, onChange }: {
  label: string;
  options: { value: T; label: ReactNode; title?: string }[];
  value: T;
  onChange: (v: T) => void;
}) {
  return (
    <div className="seg" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" title={o.title} className={"seg-btn" + (o.value === value ? " on" : "")}
          aria-pressed={o.value === value} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

const RANGE_LABELS: Record<RangeName, string> = { live: "Live", "15m": "15m", "1h": "1h", "6h": "6h", "24h": "24h", "7d": "7d", "30d": "30d" };

export function RangeControl({ live = false }: { live?: boolean }) {
  const { range, setRange } = useView();
  const names: RangeName[] = live ? ["live", "15m", "1h", "6h", "24h", "7d", "30d"] : ["15m", "1h", "6h", "24h", "7d", "30d"];
  const value = !live && range === "live" ? "1h" : range;
  return (
    <Segmented label="Time range" value={value} onChange={setRange}
      options={names.map((n) => ({ value: n, label: n === "live" ? <><span className="live-dot" />Live</> : RANGE_LABELS[n] }))} />
  );
}

const ZONES: { value: ZoneName; label: string; title: string }[] = [
  { value: "external", label: "External", title: "Internet and remote sites" },
  { value: "wan", label: "Internet", title: "Traffic through the WAN interface" },
  { value: "site", label: "Sites", title: "Traffic to remote sites over tunnels" },
  { value: "local", label: "Local", title: "Traffic to the router itself and local broadcast" },
  { value: "all", label: "All", title: "Everything the router saw" },
];

export function ZoneControl() {
  const { zone, setZone } = useView();
  return <Segmented label="Traffic scope" value={zone} onChange={setZone} options={ZONES} />;
}

export function PageHead({ title, sub, children }: { title: ReactNode; sub?: ReactNode; children?: ReactNode }) {
  return (
    <header className="page-head">
      <div>
        <h1 className="page-title">{title}</h1>
        {sub && <p className="page-sub">{sub}</p>}
      </div>
      {children && <div className="toolbar">{children}</div>}
    </header>
  );
}

export function Card({ title, note, actions, children, className = "", stale = false }: {
  title?: ReactNode; note?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string; stale?: boolean;
}) {
  return (
    <section className={`card ${className}${stale ? " stale" : ""}`}>
      {(title || actions || note) && (
        <div className="card-head">
          <div>
            {title && <h2 className="card-title">{title}</h2>}
            {note && <p className="card-note">{note}</p>}
          </div>
          {actions && <div className="card-actions">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  );
}

export function Legend() {
  return (
    <div className="legend" aria-label="Legend">
      <span className="legend-item"><span className="swatch down" />Download</span>
      <span className="legend-item"><span className="swatch up" />Upload</span>
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

export function ErrorNote({ message }: { message: string }) {
  return <div className="error-note" role="alert">{message}</div>;
}
