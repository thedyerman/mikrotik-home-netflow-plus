import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import type { RangeName, ZoneName } from "./api";
import { usePersistent } from "./hooks";

const RANGES: readonly RangeName[] = ["live", "15m", "1h", "6h", "24h", "7d", "30d"];
const ZONES: readonly ZoneName[] = ["external", "wan", "site", "local", "all"];
type Theme = "auto" | "dark" | "light";

interface ViewValue {
  range: RangeName;
  setRange: (r: RangeName) => void;
  zone: ZoneName;
  setZone: (z: ZoneName) => void;
  theme: Theme;
  setTheme: (t: Theme) => void;
  /** Resolved theme, after applying the OS preference when theme is "auto". */
  dark: boolean;
}

const ViewContext = createContext<ViewValue | null>(null);

export function useView(): ViewValue {
  const v = useContext(ViewContext);
  if (!v) throw new Error("ViewProvider missing");
  return v;
}

/** Range for pages that have no live mode. */
export const historical = (r: RangeName): Exclude<RangeName, "live"> => (r === "live" ? "1h" : r);

/** forceTheme pins the theme regardless of the saved preference (the wall display is always dark). */
export function ViewProvider({ children, forceTheme }: { children: ReactNode; forceTheme?: "dark" | "light" }) {
  const [range, setRange] = usePersistent<RangeName>("nfp-range", "live", RANGES);
  const [zone, setZone] = usePersistent<ZoneName>("nfp-zone", "external", ZONES);
  const [theme, setTheme] = usePersistent<Theme>("nfp-theme", "auto", ["auto", "dark", "light"]);
  const [osDark, setOsDark] = useState(() => window.matchMedia("(prefers-color-scheme: dark)").matches);

  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const on = () => setOsDark(mq.matches);
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);

  useEffect(() => {
    const applied = forceTheme ?? theme;
    if (applied === "auto") document.documentElement.removeAttribute("data-theme");
    else document.documentElement.setAttribute("data-theme", applied);
  }, [theme, forceTheme]);

  const dark = forceTheme ? forceTheme === "dark" : theme === "auto" ? osDark : theme === "dark";
  const value = useMemo(() => ({ range, setRange, zone, setZone, theme, setTheme, dark }), [range, setRange, zone, setZone, theme, setTheme, dark]);
  return <ViewContext.Provider value={value}>{children}</ViewContext.Provider>;
}
