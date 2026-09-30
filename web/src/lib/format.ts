// Number and time formatting shared by every view.

export function fmtRate(bps: number): { value: string; unit: string } {
  const v = Math.max(0, bps || 0);
  if (v >= 1e9) return { value: (v / 1e9).toFixed(2), unit: "Gb/s" };
  if (v >= 1e6) return { value: (v / 1e6).toFixed(v >= 1e8 ? 0 : 1), unit: "Mb/s" };
  if (v >= 1e3) return { value: (v / 1e3).toFixed(v >= 1e5 ? 0 : 1), unit: "kb/s" };
  return { value: v.toFixed(0), unit: "b/s" };
}

export function rate(bps: number): string {
  const r = fmtRate(bps);
  return `${r.value} ${r.unit}`;
}

export function fmtBytes(bytes: number): { value: string; unit: string } {
  const v = Math.max(0, bytes || 0);
  if (v >= 1e12) return { value: (v / 1e12).toFixed(2), unit: "TB" };
  if (v >= 1e9) return { value: (v / 1e9).toFixed(v >= 1e11 ? 0 : v >= 1e10 ? 1 : 2), unit: "GB" };
  if (v >= 1e6) return { value: (v / 1e6).toFixed(v >= 1e8 ? 0 : 1), unit: "MB" };
  if (v >= 1e3) return { value: (v / 1e3).toFixed(v >= 1e5 ? 0 : 1), unit: "kB" };
  return { value: v.toFixed(0), unit: "B" };
}

export function bytes(n: number): string {
  const b = fmtBytes(n);
  return `${b.value} ${b.unit}`;
}

export function count(n: number): string {
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e4) return (n / 1e3).toFixed(0) + "K";
  return Math.round(n).toLocaleString("en-US");
}

export function pct(fraction: number, digits = 0): string {
  return (fraction * 100).toFixed(digits) + "%";
}

const timeFmt = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
const shortTimeFmt = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });
const dayFmt = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });
const dayTimeFmt = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });

export const clock = (unixSec: number) => timeFmt.format(new Date(unixSec * 1000));
export const clockShort = (unixSec: number) => shortTimeFmt.format(new Date(unixSec * 1000));
export const day = (unixSec: number) => dayFmt.format(new Date(unixSec * 1000));
export const dayTime = (unixSec: number) => dayTimeFmt.format(new Date(unixSec * 1000));

export function ago(unixSec: number, nowMs = Date.now()): string {
  if (!unixSec) return "never";
  const s = Math.max(0, Math.round(nowMs / 1000 - unixSec));
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function duration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

// endpoint renders an address and port; IPv6 addresses get brackets.
export function endpoint(ip: string, port: number): string {
  if (!port) return ip;
  return ip.includes(":") ? `[${ip}]:${port}` : `${ip}:${port}`;
}
