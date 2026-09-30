// Typed client for the collector's JSON API.

export type RangeName = "live" | "15m" | "1h" | "6h" | "24h" | "7d" | "30d";
export type ZoneName = "external" | "wan" | "site" | "local" | "all";

export interface RateItem { id: number; label: string; kind?: string; down: number; up: number; conns: number }

export interface Tick {
  ts: number;
  mode: "api" | "flows";
  down: number;
  up: number;
  tail: [number, number, number][];
  conns: number;
  active: number;
  devices: number;
  topDevices: RateItem[];
  topDests: RateItem[];
  topServices: RateItem[];
  coverage: number | null;
  exportAge: number;
  firing: number;
  unacked: number;
  alertTitle?: string;
  alertSeverity?: "info" | "warning" | "critical";
  build?: string;
  routerName: string;
  apiError?: string;
  apiConfigured: boolean;
}

export interface VolItem { id: number; label: string; kind?: string; tunnel?: boolean; down: number; up: number; conns: number }

export interface Overview {
  range: string;
  from: number;
  to: number;
  step: number;
  source: "wan" | "flows";
  series: [number, number, number][];
  down: number;
  up: number;
  conns: number;
  peakDown: number;
  peakUp: number;
  topDevices: VolItem[] | null;
  topDests: VolItem[];
  topServices: VolItem[];
}

export interface LiveRow {
  dev: number; devName: string; localIp: string; localPort: number; remoteIp: string; remotePort: number; proto: string;
  remote: string; dest: number; destLabel: string; destKind: string; service: string; tunnel: boolean; zone: string; site?: string;
  down: number; up: number; bytesDown: number; bytesUp: number; state?: string;
}

export interface FlowRow {
  id: number; first: number; last: number; dev: number; devName: string; localIp: string; localPort: number;
  remoteIp: string; remotePort: number; proto: string; remote: string; dest: number; destLabel: string; destKind: string;
  service: string; tunnel: boolean; zone: string; down: number; up: number;
}

export interface DeviceInfo {
  id: number; name: string; customName: string; hostname: string; mac: string; vendor: string; ips: string[]; router: boolean;
  firstSeen: number; lastSeen: number; down: number; up: number; conns: number; liveDown: number; liveUp: number; liveConns: number;
  spark: number[];
}

export interface DeviceDetail extends Overview { device: DeviceInfo; zones: Record<string, [number, number]> }

export interface SankeyNode { id: string; col: number; ref: number; label: string; kind?: string }
export interface SankeyLink { source: string; target: string; down: number; up: number }
export interface SankeyData { nodes: SankeyNode[]; links: SankeyLink[]; total: number }

export interface AlertEvent {
  id: number; type: string; rule: string; device: number; severity: "info" | "warning" | "critical"; title: string; detail: string;
  started: number; resolved: number; firing: boolean; acked: boolean;
}
export interface RuleParam { key: string; label: string; unit: string; value: number; min: number; max: number }
export interface Rule { type: string; title: string; description: string; perDevice: boolean; enabled: boolean; params: RuleParam[]; muted: number[] }

export interface Coverage { known: boolean; total: number; down: number; up: number; seconds: number }
export interface Status {
  app: string; version: string; now: number;
  engine: {
    mode: string; started: number; lastExport: number; exportAge: number; apiEnabled: boolean; apiUp: boolean; apiError: string; apiSince: number;
    router: { identity: string; version: string; board: string; uptime: string; cpuLoad: number } | null;
    interfaces: { index: number; name: string; type: string; wan: boolean; running: boolean; rxBps: number; txBps: number }[];
    localNets: string[]; sites: { name: string; prefix: string }[]; routerAddrs: string[]; wan: string[];
    coverage: Coverage; devices: number; openConns: number; trackedConns: number; naming: Record<string, number>; dnsNames: number;
    asnDatabase: string; asnBuilt: number; learning: boolean;
  };
  decoder: { messages: number; records: number; templateSets: number; unknownTemplate: number; errors: number; lostRecords: number; sequenceResets: number; templates: number };
  clocks: { exporter: string; offsetMs: number; bootTime: string; reboots: number }[] | null;
  udp: { listen: string; packets: number; rejected: number; exporters: string[] | null };
  store: { bytes: number; maxBytes: number; tables: Record<string, number>; retention: Record<string, string>; foldBelow: number };
  routerApi: { addr: string; tls: boolean; fingerprint: string };
  setup: { collector: string; flowPort: number; activeTimeoutSeconds: number };
  attribution: string;
}

export interface Today { since: number; down: number; up: number; conns: number; peakDown: number; peakUp: number }

export interface Session { authRequired: boolean; authenticated: boolean; app: string; version: string }

export class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

// Listeners are told when any request comes back 401, so the app can show the login form.
const unauthorized = new Set<() => void>();
export const onUnauthorized = (fn: () => void) => { unauthorized.add(fn); return () => { unauthorized.delete(fn); }; };

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, {
    method, signal, credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).error ?? msg; } catch { /* not JSON */ }
    if (res.status === 401 && !path.endsWith("/login")) unauthorized.forEach((fn) => fn());
    throw new ApiError(res.status, msg);
  }
  return res.json() as Promise<T>;
}

export const get = <T,>(path: string, signal?: AbortSignal) => request<T>("GET", path, undefined, signal);
export const post = <T,>(path: string, body?: unknown) => request<T>("POST", path, body ?? {});
export const put = <T,>(path: string, body: unknown) => request<T>("PUT", path, body);
export const patch = <T,>(path: string, body: unknown) => request<T>("PATCH", path, body);

export function query(params: Record<string, string | number | undefined | null>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
  const s = q.toString();
  return s ? "?" + s : "";
}
