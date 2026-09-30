import type { ReactNode } from "react";
import { post } from "../lib/api";
import { href, useRoute } from "../lib/hooks";
import { useLive } from "../lib/live";
import { useView } from "../lib/view";
import { IconAuto, IconBell, IconDevices, IconDisplay, IconFlows, IconLogout, IconMap, IconMoon, IconOverview, IconServer, IconSun } from "./Icons";

const NAV = [
  { path: "/", label: "Overview", icon: IconOverview },
  { path: "/flows", label: "Flows", icon: IconFlows },
  { path: "/devices", label: "Devices", icon: IconDevices },
  { path: "/map", label: "Flow map", icon: IconMap },
  { path: "/alerts", label: "Alerts", icon: IconBell },
  { path: "/status", label: "Status", icon: IconServer },
];

export function Shell({ children, canLogout }: { children: ReactNode; canLogout: boolean }) {
  const route = useRoute();
  const { tick, connected } = useLive();
  const { theme, setTheme } = useView();
  const section = "/" + (route.segments[0] ?? "");

  let state: { cls: string; label: string; detail: string };
  if (!connected) state = { cls: "off", label: "Offline", detail: "Reconnecting to the collector" };
  else if (!tick) state = { cls: "wait", label: "Connecting", detail: "" };
  else if (tick.exportAge < 0 && tick.mode === "flows") state = { cls: "wait", label: "Waiting", detail: "No flow records received yet" };
  else if (tick.mode === "api") state = { cls: "live", label: "Live", detail: "Router API, about 1 s behind" };
  else state = { cls: "delayed", label: "Delayed", detail: tick.apiConfigured ? "Router API unreachable; flow records only" : "Flow records only, 15–75 s behind" };

  const nextTheme = theme === "auto" ? "dark" : theme === "dark" ? "light" : "auto";
  const ThemeIcon = theme === "auto" ? IconAuto : theme === "dark" ? IconMoon : IconSun;

  return (
    <div className="app">
      <aside className="sidebar">
        <a className="brand" href={href("/")}>
          <svg className="brand-mark" viewBox="0 0 32 32" aria-hidden="true">
            <path d="M5 20c4.5 0 4.5-9 9-9s4.5 9 9 9 4-5.5 4-5.5" fill="none" stroke="var(--down)" strokeWidth="2.6" strokeLinecap="round" />
            <circle cx="27" cy="14.5" r="2.6" fill="var(--up)" />
          </svg>
          <span>
            <span className="brand-name">NetFlow Plus</span>
            <span className="brand-sub">{tick?.routerName || "MikroTik"}</span>
          </span>
        </a>
        <nav className="nav" aria-label="Sections">
          {NAV.map((n) => {
            const active = n.path === "/" ? section === "/" : section === n.path;
            return (
              <a key={n.path} href={href(n.path)} className={"nav-link" + (active ? " active" : "")} aria-current={active ? "page" : undefined}>
                <n.icon className="nav-icon" />
                <span>{n.label}</span>
                {n.path === "/alerts" && tick && tick.unacked > 0 && (
                  <span className={"nav-badge" + (tick.firing > 0 ? " firing" : "")}>{tick.unacked > 99 ? "99+" : tick.unacked}</span>
                )}
              </a>
            );
          })}
        </nav>
        <div className="side-foot">
          <div className={"conn-state " + state.cls} title={state.detail}>
            <span className="state-dot" />
            <span className="state-label">{state.label}</span>
            <span className="state-detail">{state.detail}</span>
          </div>
          <div className="side-actions">
            <button type="button" className="icon-btn" onClick={() => setTheme(nextTheme)} title={`Theme: ${theme}. Switch to ${nextTheme}.`} aria-label={`Theme: ${theme}. Switch to ${nextTheme}.`}>
              <ThemeIcon />
            </button>
            <a className="icon-btn" href="/dashboard" target="_blank" rel="noreferrer" title="Open the wall display" aria-label="Open the wall display">
              <IconDisplay />
            </a>
            {canLogout && (
              <button type="button" className="icon-btn" title="Sign out" aria-label="Sign out"
                onClick={() => post("/api/v1/logout").finally(() => window.location.reload())}>
                <IconLogout />
              </button>
            )}
          </div>
        </div>
      </aside>
      <main className="main">{children}</main>
    </div>
  );
}
