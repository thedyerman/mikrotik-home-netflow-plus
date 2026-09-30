import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { get } from "./api";

export interface FetchState<T> {
  data: T | undefined;
  error: string | null;
  /** True while a request for a different URL is in flight; the previous data stays on screen. */
  stale: boolean;
  reload: () => void;
}

// useFetch loads JSON and optionally re-polls it. While new data is loading
// the previous result is kept, so views dim instead of flashing empty.
export function useFetch<T>(url: string | null, refreshMs = 0): FetchState<T> {
  const [state, setState] = useState<{ data?: T; error: string | null; url: string | null }>({ error: null, url: null });
  const [nonce, setNonce] = useState(0);
  const reload = useCallback(() => setNonce((n) => n + 1), []);

  useEffect(() => {
    if (!url) return;
    const ctl = new AbortController();
    let timer: number | undefined;
    const load = () => {
      get<T>(url, ctl.signal)
        .then((data) => setState({ data, error: null, url }))
        .catch((e: Error) => {
          if (e.name !== "AbortError") setState((s) => ({ ...s, error: e.message, url }));
        })
        .finally(() => {
          if (refreshMs > 0 && !ctl.signal.aborted) {
            timer = window.setTimeout(() => (document.hidden ? wait() : load()), refreshMs);
          }
        });
    };
    // A hidden tab stops polling and resumes when it becomes visible again.
    const wait = () => {
      const onVis = () => {
        if (!document.hidden) {
          document.removeEventListener("visibilitychange", onVis);
          if (!ctl.signal.aborted) load();
        }
      };
      document.addEventListener("visibilitychange", onVis);
      ctl.signal.addEventListener("abort", () => document.removeEventListener("visibilitychange", onVis));
    };
    load();
    return () => {
      ctl.abort();
      window.clearTimeout(timer);
    };
  }, [url, refreshMs, nonce]);

  return { data: state.data, error: state.error, stale: state.url !== url && state.data !== undefined, reload };
}

// ---- hash router ----

export interface Route {
  path: string;
  segments: string[];
  params: URLSearchParams;
}

function parseHash(): Route {
  const raw = window.location.hash.replace(/^#/, "") || "/";
  const [path, qs = ""] = raw.split("?");
  return { path, segments: path.split("/").filter(Boolean), params: new URLSearchParams(qs) };
}

const subscribeHash = (fn: () => void) => {
  window.addEventListener("hashchange", fn);
  return () => window.removeEventListener("hashchange", fn);
};
const hashSnapshot = () => window.location.hash;

export function useRoute(): Route {
  useSyncExternalStore(subscribeHash, hashSnapshot);
  return parseHash();
}

export function href(path: string, params?: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  if (params) for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "") q.set(k, String(v));
  const s = q.toString();
  return "#" + path + (s ? "?" + s : "");
}

export function navigate(path: string, params?: Record<string, string | number | undefined>) {
  window.location.hash = href(path, params).slice(1);
}

// ---- small utilities ----

// usePersistent is useState backed by localStorage.
export function usePersistent<T extends string>(key: string, initial: T, allowed?: readonly T[]): [T, (v: T) => void] {
  const [value, setValue] = useState<T>(() => {
    try {
      const v = localStorage.getItem(key) as T | null;
      if (v !== null && (!allowed || allowed.includes(v))) return v;
    } catch { /* storage unavailable */ }
    return initial;
  });
  const set = useCallback((v: T) => {
    setValue(v);
    try { localStorage.setItem(key, v); } catch { /* storage unavailable */ }
  }, [key]);
  return [value, set];
}

// useNow re-renders on an interval, for relative timestamps.
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(t);
  }, [intervalMs]);
  return now;
}

// useTween eases a displayed number toward its target so live figures glide
// instead of jumping. It honours the reduced-motion preference.
export function useTween(target: number, ms = 600): number {
  const [value, setValue] = useState(target);
  const from = useRef(target);
  const current = useRef(target);
  useEffect(() => {
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      current.current = target;
      setValue(target);
      return;
    }
    from.current = current.current;
    const start = performance.now();
    let raf = 0;
    const step = (t: number) => {
      const k = Math.min(1, (t - start) / ms);
      const eased = 1 - Math.pow(1 - k, 3);
      current.current = from.current + (target - from.current) * eased;
      setValue(current.current);
      if (k < 1) raf = requestAnimationFrame(step);
    };
    raf = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf);
  }, [target, ms]);
  return value;
}

// useElementSize tracks an element's content box, for canvases that fill their container.
export function useElementSize<T extends HTMLElement>(): [React.RefObject<T | null>, { width: number; height: number }] {
  const ref = useRef<T | null>(null);
  const [size, setSize] = useState({ width: 0, height: 0 });
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      const r = entries[0].contentRect;
      setSize({ width: Math.floor(r.width), height: Math.floor(r.height) });
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, size];
}

// useElementWidth tracks an element's width for responsive SVG and canvas.
export function useElementWidth<T extends HTMLElement>(): [React.RefObject<T | null>, number] {
  const ref = useRef<T | null>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => setWidth(Math.floor(entries[0].contentRect.width)));
    ro.observe(el);
    setWidth(Math.floor(el.getBoundingClientRect().width));
    return () => ro.disconnect();
  }, []);
  return [ref, width];
}
