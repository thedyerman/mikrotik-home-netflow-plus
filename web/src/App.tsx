import { useEffect, useState } from "react";
import { get, onUnauthorized, type Session } from "./lib/api";
import { useRoute } from "./lib/hooks";
import { LiveProvider } from "./lib/live";
import { ViewProvider } from "./lib/view";
import { Shell } from "./components/Shell";
import { TipHost } from "./components/Tip";
import { Alerts } from "./pages/Alerts";
import { DevicePage } from "./pages/DevicePage";
import { Devices } from "./pages/Devices";
import { FlowMap } from "./pages/FlowMap";
import { Flows } from "./pages/Flows";
import { Login } from "./pages/Login";
import { Overview } from "./pages/Overview";
import { StatusPage } from "./pages/Status";

function Pages() {
  const route = useRoute();
  const [section, id] = route.segments;
  switch (section) {
    case undefined: return <Overview />;
    case "flows": return <Flows />;
    case "devices": return id ? <DevicePage id={Number(id)} /> : <Devices />;
    case "map": return <FlowMap />;
    case "alerts": return <Alerts />;
    case "status": return <StatusPage />;
    default: return <Overview />;
  }
}

export function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [error, setError] = useState<string | null>(null);

  const check = () => get<Session>("/api/v1/session").then(setSession).catch((e: Error) => setError(e.message));
  useEffect(() => {
    check();
    return onUnauthorized(() => setSession((s) => (s ? { ...s, authenticated: false } : s)));
  }, []);

  if (error && !session) {
    return <div className="login"><div className="card login-card"><h1 className="page-title">Cannot reach the collector</h1><p className="page-sub">{error}</p><button className="btn" onClick={() => { setError(null); check(); }}>Try again</button></div></div>;
  }
  if (!session) return null;
  return (
    <ViewProvider>
      {session.authRequired && !session.authenticated ? (
        <Login onDone={check} />
      ) : (
        <LiveProvider>
          <Shell canLogout={session.authRequired}>
            <Pages />
          </Shell>
          <TipHost />
        </LiveProvider>
      )}
    </ViewProvider>
  );
}
