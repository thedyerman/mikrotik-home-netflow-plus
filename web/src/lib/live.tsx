import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { get, type Tick } from "./api";

const KEEP_SECONDS = 900;

interface LiveValue {
  tick: Tick | null;
  connected: boolean;
  /** Per-second WAN rates, oldest first: [unix seconds, download bps, upload bps]. */
  series: [number, number, number][];
}

const LiveContext = createContext<LiveValue>({ tick: null, connected: false, series: [] });

export const useLive = () => useContext(LiveContext);

// LiveProvider holds the WebSocket that delivers one tick per second, and the
// rolling per-second series the live chart draws.
export function LiveProvider({ children }: { children: ReactNode }) {
  const [tick, setTick] = useState<Tick | null>(null);
  const [connected, setConnected] = useState(false);
  const samples = useRef(new Map<number, [number, number]>());
  const [version, setVersion] = useState(0);

  useEffect(() => {
    let ws: WebSocket | null = null;
    let closed = false;
    let retry = 1000;
    let timer: number | undefined;

    const merge = (rows: [number, number, number][]) => {
      const m = samples.current;
      for (const [ts, d, u] of rows) m.set(ts, [d, u]);
      const cutoff = Date.now() / 1000 - KEEP_SECONDS;
      for (const ts of m.keys()) if (ts < cutoff) m.delete(ts);
      setVersion((v) => v + 1);
    };

    const seed = () =>
      get<{ series: [number, number, number][] }>("/api/v1/live/series?seconds=600")
        .then((r) => merge(r.series))
        .catch(() => undefined);

    const connect = () => {
      const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
      ws = new WebSocket(`${proto}//${window.location.host}/api/v1/live`);
      ws.onopen = () => {
        retry = 1000;
        setConnected(true);
        seed();
      };
      ws.onmessage = (ev) => {
        const t = JSON.parse(ev.data) as Tick;
        setTick(t);
        merge(t.tail);
      };
      ws.onclose = () => {
        setConnected(false);
        if (closed) return;
        timer = window.setTimeout(connect, retry);
        retry = Math.min(retry * 2, 15000);
      };
      ws.onerror = () => ws?.close();
    };
    connect();
    return () => {
      closed = true;
      window.clearTimeout(timer);
      ws?.close();
    };
  }, []);

  const series = useMemo(() => {
    const out: [number, number, number][] = [];
    for (const [ts, [d, u]] of samples.current) out.push([ts, d, u]);
    out.sort((a, b) => a[0] - b[0]);
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [version]);

  const value = useMemo(() => ({ tick, connected, series }), [tick, connected, series]);
  return <LiveContext.Provider value={value}>{children}</LiveContext.Provider>;
}
