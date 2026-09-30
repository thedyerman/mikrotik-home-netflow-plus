import { useEffect, useState } from "react";
import { post, put, type AlertEvent, type Rule } from "../lib/api";
import { ago, dayTime, duration } from "../lib/format";
import { href, useFetch, useNow } from "../lib/hooks";
import { Card, ErrorNote, PageHead } from "../components/Controls";
import { IconCritical, IconInfo, IconWarn } from "../components/Icons";

const SEVERITY = {
  info: { label: "Info", icon: IconInfo },
  warning: { label: "Warning", icon: IconWarn },
  critical: { label: "Critical", icon: IconCritical },
} as const;

export function EventList({ events, now }: { events: AlertEvent[]; now: number }) {
  return (
    <ul className="events">
      {events.map((e) => {
        const sev = SEVERITY[e.severity] ?? SEVERITY.info;
        return (
          <li key={e.id} className={"event sev-" + e.severity + (e.firing ? " firing" : "") + (e.acked ? "" : " unread")}>
            <span className="event-icon" title={sev.label}><sev.icon /><span className="sr-only">{sev.label}</span></span>
            <div className="event-body">
              <div className="event-title">
                {e.title}
                {e.firing && <span className="tag firing">Active</span>}
              </div>
              {e.detail && <div className="event-detail">{e.detail}</div>}
              <div className="event-meta">
                <span>{e.rule || e.type}</span>
                <span title={dayTime(e.started)}>{ago(e.started, now)}</span>
                {!e.firing && e.resolved > e.started && <span>lasted {duration(e.resolved - e.started)}</span>}
                {e.device > 0 && <a className="link" href={href(`/devices/${e.device}`)}>View device</a>}
              </div>
            </div>
          </li>
        );
      })}
    </ul>
  );
}

function RuleCard({ rule, onChange }: { rule: Rule; onChange: (rules: Rule[]) => void }) {
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(rule.params.map((p) => [p.key, String(p.value)])));
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const dirty = rule.params.some((p) => Number(values[p.key]) !== p.value);

  const save = (enabled: boolean) => {
    const params = Object.fromEntries(rule.params.map((p) => [p.key, Number(values[p.key])]));
    if (Object.values(params).some((v) => !Number.isFinite(v))) {
      setError("Enter a number for every setting.");
      return;
    }
    put<{ rules: Rule[] }>(`/api/v1/alerts/rules/${rule.type}`, { enabled, params })
      .then((r) => {
        setError(null);
        setSaved(true);
        window.setTimeout(() => setSaved(false), 1500);
        onChange(r.rules);
        const mine = r.rules.find((x) => x.type === rule.type);
        if (mine) setValues(Object.fromEntries(mine.params.map((p) => [p.key, String(p.value)])));
      })
      .catch((e: Error) => setError(e.message));
  };

  return (
    <div className={"rule" + (rule.enabled ? "" : " disabled")}>
      <label className="switch">
        <input type="checkbox" checked={rule.enabled} onChange={(e) => save(e.target.checked)} aria-label={`Enable ${rule.title}`} />
        <span className="switch-track" />
      </label>
      <div className="rule-body">
        <div className="rule-title">{rule.title}</div>
        <div className="rule-desc">{rule.description}</div>
        {rule.params.length > 0 && (
          <div className="rule-params">
            {rule.params.map((p) => (
              <label key={p.key} className="field inline">
                <span>{p.label}</span>
                <input className="input num" type="number" min={p.min} max={p.max} step="any" value={values[p.key] ?? ""}
                  onChange={(e) => setValues({ ...values, [p.key]: e.target.value })} />
                {p.unit && <span className="dim">{p.unit}</span>}
              </label>
            ))}
            <button type="button" className="btn small" disabled={!dirty} onClick={() => save(rule.enabled)}>{saved ? "Saved" : "Save"}</button>
          </div>
        )}
        {error && <div className="error-inline">{error}</div>}
      </div>
    </div>
  );
}

function Webhook() {
  const res = useFetch<{ webhookUrl: string }>("/api/v1/settings");
  const [url, setUrl] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  useEffect(() => { if (res.data) setUrl(res.data.webhookUrl); }, [res.data]);

  const save = () =>
    put<{ webhookUrl: string }>("/api/v1/settings", { webhookUrl: url })
      .then(() => { setMsg({ ok: true, text: url ? "Saved." : "Webhook removed." }); res.reload(); })
      .catch((e: Error) => setMsg({ ok: false, text: e.message }));
  const test = () =>
    post("/api/v1/settings/test-webhook")
      .then(() => setMsg({ ok: true, text: "Test notification delivered." }))
      .catch((e: Error) => setMsg({ ok: false, text: "Delivery failed: " + e.message }));

  return (
    <Card title="Notifications" note="Every alert is also sent as JSON to this webhook. Works with ntfy, Slack, Discord and Home Assistant.">
      <div className="form-row">
        <input className="input grow" type="url" placeholder="https://ntfy.sh/your-topic" value={url} onChange={(e) => setUrl(e.target.value)} aria-label="Webhook URL" />
        <button type="button" className="btn" onClick={save} disabled={url === (res.data?.webhookUrl ?? "")}>Save</button>
        <button type="button" className="btn" onClick={test} disabled={!res.data?.webhookUrl}>Send test</button>
      </div>
      {msg && <div className={msg.ok ? "ok-inline" : "error-inline"} role="status">{msg.text}</div>}
    </Card>
  );
}

export function Alerts() {
  const events = useFetch<{ events: AlertEvent[]; firing: number; unacked: number }>("/api/v1/alerts/events?limit=200", 10_000);
  const rulesRes = useFetch<{ rules: Rule[] }>("/api/v1/alerts/rules");
  const [rules, setRules] = useState<Rule[] | null>(null);
  const now = useNow(5000);
  useEffect(() => { if (rulesRes.data) setRules(rulesRes.data.rules); }, [rulesRes.data]);

  const list = events.data?.events ?? [];
  const markRead = () => post("/api/v1/alerts/ack", { id: 0 }).then(events.reload);

  return (
    <div className="page">
      <PageHead title="Alerts" sub={events.data ? `${events.data.firing} active, ${events.data.unacked} unread` : undefined}>
        <button type="button" className="btn" onClick={markRead} disabled={!events.data || events.data.unacked === 0}>Mark all read</button>
      </PageHead>
      {events.error && <ErrorNote message={events.error} />}
      <div className="grid-2 wide-left">
        <Card title="Events" note="Newest first. Resolved events are kept for 30 days.">
          {list.length > 0 ? <EventList events={list} now={now} /> : (
            <div className="empty">
              {events.data ? "Nothing to report. New devices are announced after a 20-minute learning period on first start." : "Loading…"}
            </div>
          )}
        </Card>
        <div className="stack">
          <Card title="Rules">
            {rulesRes.error && <ErrorNote message={rulesRes.error} />}
            <div className="rules">
              {(rules ?? []).map((r) => <RuleCard key={r.type} rule={r} onChange={setRules} />)}
            </div>
          </Card>
          <Webhook />
        </div>
      </div>
    </div>
  );
}
