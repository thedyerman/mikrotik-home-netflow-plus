import { useEffect, useRef, useSyncExternalStore, type PointerEvent, type ReactNode } from "react";

// One floating tooltip for the whole app. Marks call tip(content) to get
// pointer handlers; the host renders next to the pointer.

interface TipState { x: number; y: number; content: ReactNode | null }

let state: TipState = { x: 0, y: 0, content: null };
const listeners = new Set<() => void>();
const set = (next: TipState) => { state = next; listeners.forEach((l) => l()); };
const subscribe = (l: () => void) => { listeners.add(l); return () => { listeners.delete(l); }; };

/** Pointer and focus handlers that show `content` while the element is hovered or focused. */
export function tip(content: () => ReactNode) {
  return {
    onPointerMove: (e: PointerEvent) => set({ x: e.clientX, y: e.clientY, content: content() }),
    onPointerLeave: () => set({ ...state, content: null }),
    onFocus: (e: { currentTarget: Element }) => {
      const r = e.currentTarget.getBoundingClientRect();
      set({ x: r.left + r.width / 2, y: r.top, content: content() });
    },
    onBlur: () => set({ ...state, content: null }),
  };
}

export function TipHost() {
  const s = useSyncExternalStore(subscribe, () => state);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el || !s.content) return;
    const { width, height } = el.getBoundingClientRect();
    let x = s.x + 14;
    let y = s.y + 14;
    if (x + width > window.innerWidth - 8) x = s.x - width - 14;
    if (y + height > window.innerHeight - 8) y = s.y - height - 14;
    el.style.transform = `translate(${Math.max(8, x)}px, ${Math.max(8, y)}px)`;
  });
  if (!s.content) return null;
  return <div ref={ref} className="tip" role="tooltip">{s.content}</div>;
}

/** Standard tooltip body: a heading and value-first rows with a colour key. */
export function TipBody({ title, rows }: { title: ReactNode; rows: { key?: "down" | "up" | "none"; label: string; value: string }[] }) {
  return (
    <>
      <div className="tip-head">{title}</div>
      {rows.map((r) => (
        <div className="tip-row" key={r.label}>
          {r.key && r.key !== "none" ? <span className={`key-line ${r.key}`} /> : <span className="key-line none" />}
          <span className="tip-val">{r.value}</span>
          <span className="tip-label">{r.label}</span>
        </div>
      ))}
    </>
  );
}
