// Helpers that make live numbers read calmly: unit hysteresis, smoothing,
// a slowly decaying bar scale, sticky ordering and reorder animation.
//
// The server already smooths the rates it sends (fast attack, slow release);
// what is left here is presentation: keeping units, widths, scales and row
// order from twitching on every tick.
import { useLayoutEffect, useMemo, useRef, type RefObject } from "react";

// ---- units with hysteresis ----

export const RATE_UNITS = ["b/s", "kb/s", "Mb/s", "Gb/s"] as const;

function naturalUnit(bps: number): number {
  if (bps >= 1e9) return 3;
  if (bps >= 1e6) return 2;
  if (bps >= 1e3) return 1;
  return 0;
}

/** Picks the unit for a rate, only leaving the previous unit once the value is clearly past the boundary. */
export function unitIndexFor(bps: number, prev?: number): number {
  let i = prev ?? naturalUnit(bps);
  while (i < 3 && bps >= 1.2 * 1000 ** (i + 1)) i++;
  while (i > 0 && bps < 0.8 * 1000 ** i) i--;
  return i;
}

export function rateIn(bps: number, unit: number): { value: string; unit: string } {
  const v = Math.max(0, bps) / 1000 ** unit;
  return { value: v >= 100 ? v.toFixed(0) : v.toFixed(1), unit: RATE_UNITS[unit] };
}

/** A per-key memory of the unit each value was last shown in. */
export class UnitHold {
  private units = new Map<string, number>();
  format(key: string, bps: number): { value: string; unit: string } {
    const i = unitIndexFor(bps, this.units.get(key));
    this.units.set(key, i);
    return rateIn(bps, i);
  }
  forget(keep: Set<string>) {
    for (const k of this.units.keys()) if (!keep.has(k)) this.units.delete(k);
  }
}

/** A single value's unit with hysteresis, for the big figures. */
export function useStableRate(bps: number): { value: string; unit: string } {
  const unit = useRef<number | undefined>(undefined);
  unit.current = unitIndexFor(bps, unit.current);
  return rateIn(bps, unit.current);
}

// ---- smoothing ----

/** Moves y towards x over dt seconds: quickly when rising, slowly when falling. */
export function smooth(y: number, x: number, dt: number, attack: number, release: number): number {
  if (dt <= 0) return y;
  const tau = x > y ? attack : release;
  return y + (1 - Math.exp(-dt / tau)) * (x - y);
}

/** Smooths a value across renders with the given time constants (seconds). */
export function useSmooth(value: number, attack: number, release: number): number {
  const state = useRef<{ y: number; at: number } | null>(null);
  const now = performance.now() / 1000;
  if (!state.current) state.current = { y: value, at: now };
  else {
    state.current.y = smooth(state.current.y, value, now - state.current.at, attack, release);
    state.current.at = now;
  }
  return state.current.y;
}

// ---- sticky ordering ----

/**
 * Orders keys by value, but a key only overtakes the one above it when it
 * is ahead by both a ratio and an absolute margin. Near-ties keep their
 * previous order, so rankings stop flickering while real changes still
 * move rows, in one step if the difference is large.
 */
export function stableOrder(prev: string[], values: Map<string, number>, ratio = 1.2, margin = 20_000): string[] {
  const order = prev.filter((k) => values.has(k));
  const seen = new Set(order);
  const fresh = [...values.keys()].filter((k) => !seen.has(k)).sort((a, b) => values.get(b)! - values.get(a)!);
  order.push(...fresh);
  let swapped = true;
  for (let pass = 0; swapped && pass < order.length; pass++) {
    swapped = false;
    for (let i = 1; i < order.length; i++) {
      const lo = values.get(order[i - 1])!, hi = values.get(order[i])!;
      if (hi > lo * ratio && hi > lo + margin) {
        [order[i - 1], order[i]] = [order[i], order[i - 1]];
        swapped = true;
      }
    }
  }
  return order;
}

export interface RateLike { id: number; label: string; down: number; up: number }

export interface LiveRankingOptions {
  /** Extra client-side smoothing on top of the server's, in seconds (0 for none). */
  attack: number;
  release: number;
  /** Seconds the bar scale holds its peak before decaying. */
  hold?: number;
  ratio?: number;
  margin?: number;
}

/**
 * Turns a stream of ranked items into a calm list: values smoothed, order
 * sticky, and a bar scale that decays slowly from its peak instead of
 * rescaling every time the leader changes.
 */
export function useLiveRanking<T extends RateLike>(items: T[], opts: LiveRankingOptions): { items: T[]; max: number } {
  const state = useRef({ values: new Map<string, { down: number; up: number; at: number }>(), order: [] as string[], peak: 0, peakAt: 0 });
  return useMemo(() => {
    const s = state.current;
    const now = performance.now() / 1000;
    const key = (it: T) => `${it.id}:${it.label}`;
    const keep = new Set<string>();
    const smoothed = items.map((it) => {
      const k = key(it);
      keep.add(k);
      const prev = s.values.get(k);
      let down = it.down, up = it.up;
      if (prev && opts.attack > 0) {
        down = smooth(prev.down, it.down, now - prev.at, opts.attack, opts.release);
        up = smooth(prev.up, it.up, now - prev.at, opts.attack, opts.release);
      }
      s.values.set(k, { down, up, at: now });
      return { ...it, down, up };
    });
    for (const k of s.values.keys()) if (!keep.has(k)) s.values.delete(k);
    const totals = new Map(smoothed.map((it) => [key(it), it.down + it.up]));
    s.order = stableOrder(s.order, totals, opts.ratio, opts.margin);
    const byKey = new Map(smoothed.map((it) => [key(it), it]));
    const ordered = s.order.map((k) => byKey.get(k)!);

    const current = Math.max(0, ...smoothed.map((it) => it.down + it.up));
    const hold = opts.hold ?? 10;
    const decayed = s.peak * Math.exp(-(now - s.peakAt) / hold);
    if (current >= decayed) {
      s.peak = current;
      s.peakAt = now;
    }
    return { items: ordered, max: Math.max(current, decayed, 1) };
  }, [items, opts.attack, opts.release, opts.hold, opts.ratio, opts.margin]);
}

// ---- reorder animation ----

/**
 * Animates rows that change position: every element with a data-flip
 * attribute inside the container slides from where it was to where it is.
 */
export function useFlip(ref: RefObject<HTMLElement | null>, deps: unknown) {
  const prev = useRef(new Map<string, number>());
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const base = el.getBoundingClientRect().top;
    const next = new Map<string, number>();
    const moves: [HTMLElement, number][] = [];
    el.querySelectorAll<HTMLElement>("[data-flip]").forEach((c) => {
      const k = c.dataset.flip!;
      const top = c.getBoundingClientRect().top - base;
      next.set(k, top);
      const old = prev.current.get(k);
      if (old !== undefined && Math.abs(old - top) > 0.5) moves.push([c, old - top]);
    });
    prev.current = next;
    if (moves.length === 0 || window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    for (const [c, dy] of moves) {
      c.style.transition = "none";
      c.style.transform = `translateY(${dy}px)`;
    }
    void el.offsetHeight; // commit the start position before animating to the end
    requestAnimationFrame(() => {
      for (const [c] of moves) {
        c.style.transition = "transform 320ms ease";
        c.style.transform = "";
      }
    });
  }, [ref, deps]);
}
