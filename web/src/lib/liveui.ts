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
 * has been clearly ahead (by a ratio and an absolute margin) for `persist`
 * consecutive calls. Near-ties and brief crossings keep their previous
 * order, so rankings stop flickering, while a real change still moves a row
 * as far as it belongs in one step. New keys start at the bottom and earn
 * their way up like any other row. The margin grows with the largest value,
 * so the tail of a list led by a big transfer does not shuffle over a few
 * kb/s.
 *
 * `pending` carries the per-key count of consecutive calls spent clearly
 * ahead between calls; pass the same Map every time.
 */
export function stableOrder(prev: string[], values: Map<string, number>, ratio = 1.2, margin = 20_000, persist = 1, pending?: Map<string, number>): string[] {
  const order = prev.filter((k) => values.has(k));
  const seen = new Set(order);
  const fresh = [...values.keys()].filter((k) => !seen.has(k)).sort((a, b) => values.get(b)! - values.get(a)!);
  order.push(...fresh);
  const top = Math.max(0, ...values.values());
  const gap = Math.max(margin, top * 0.03);
  const ahead = (hi: string, lo: string) => {
    const h = values.get(hi)!, l = values.get(lo)!;
    return h > l * ratio && h > l + gap;
  };
  const ready = new Set<string>();
  if (pending && persist > 1) {
    for (let i = 1; i < order.length; i++) {
      const k = order[i];
      const n = ahead(k, order[i - 1]) ? (pending.get(k) ?? 0) + 1 : 0;
      pending.set(k, n);
      if (n >= persist) ready.add(k);
    }
    for (const k of pending.keys()) if (!values.has(k)) pending.delete(k);
  } else {
    for (const k of order) ready.add(k);
  }
  let swapped = true;
  for (let pass = 0; swapped && pass < order.length; pass++) {
    swapped = false;
    for (let i = 1; i < order.length; i++) {
      if (ready.has(order[i]) && ahead(order[i], order[i - 1])) {
        [order[i - 1], order[i]] = [order[i], order[i - 1]];
        swapped = true;
      }
    }
  }
  for (const k of ready) pending?.set(k, 0);
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
  /** Consecutive updates a row must be clearly ahead before it overtakes. */
  persist?: number;
}

// Rows are ordered by a slower average than the number they display, the way
// iftop sorts by its 10-second column: the figure follows the traffic, the
// position reflects the last quarter of a minute.
const rankAttack = 3;
const rankRelease = 15;
const lingerFloor = 2_000; // bits per second below which a departed row is dropped
const lingerMax = 12; // seconds a departed row may stay while fading
const settleSeconds = 4; // a new row is shown only once it has been around this long

/**
 * Turns a stream of ranked items into a calm list: values smoothed, order
 * sticky, rows that leave fading out instead of vanishing, and a bar scale
 * that decays slowly from its peak instead of rescaling every time the
 * leader changes.
 */
export function useLiveRanking<T extends RateLike>(items: T[], opts: LiveRankingOptions): { items: T[]; max: number } {
  const state = useRef({
    values: new Map<string, { item: T; down: number; up: number; rank: number; at: number; seenAt: number; firstAt: number }>(),
    order: [] as string[], pending: new Map<string, number>(), peak: 0, peakAt: 0,
  });
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
      const rank = prev ? smooth(prev.rank, down + up, now - prev.at, rankAttack, rankRelease) : down + up;
      s.values.set(k, { item: it, down, up, rank, at: now, seenAt: now, firstAt: prev?.firstAt ?? now });
      return { ...it, down, up };
    });
    // A row that has only just appeared may be a two-second burst (a DNS
    // lookup, a keep-alive); it is listed once it has stayed for a moment.
    const settled = (k: string) => now - (s.values.get(k)?.firstAt ?? now) >= settleSeconds;
    // Rows that left the data keep fading for a while, so they slide down and
    // out instead of disappearing from the middle of the list.
    for (const [k, v] of s.values) {
      if (keep.has(k)) continue;
      v.down = smooth(v.down, 0, now - v.at, opts.attack || 1, opts.release || 4);
      v.up = smooth(v.up, 0, now - v.at, opts.attack || 1, opts.release || 4);
      v.rank = smooth(v.rank, 0, now - v.at, rankAttack, rankRelease);
      v.at = now;
      if (v.down + v.up < lingerFloor || now - v.seenAt > lingerMax) {
        s.values.delete(k);
        continue;
      }
      smoothed.push({ ...v.item, down: v.down, up: v.up });
    }
    const ranks = new Map(smoothed.map((it) => { const k = key(it); return [k, s.values.get(k)?.rank ?? it.down + it.up]; }));
    s.order = stableOrder(s.order, ranks, opts.ratio, opts.margin, opts.persist ?? 3, s.pending);
    const byKey = new Map(smoothed.map((it) => [key(it), it]));
    const ordered = s.order.filter(settled).map((k) => byKey.get(k)!);

    const current = Math.max(0, ...smoothed.map((it) => it.down + it.up));
    const hold = opts.hold ?? 10;
    const decayed = s.peak * Math.exp(-(now - s.peakAt) / hold);
    if (current >= decayed) {
      s.peak = current;
      s.peakAt = now;
    }
    return { items: ordered, max: Math.max(current, decayed, 1) };
  }, [items, opts.attack, opts.release, opts.hold, opts.ratio, opts.margin, opts.persist]);
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
