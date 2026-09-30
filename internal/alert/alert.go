// Package alert evaluates a small fixed set of rules against what the engine
// observes, keeps an event log and delivers events to a webhook.
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"mikrotik-home-netflow-plus/internal/store"
)

// Rule types.
const (
	NewDevice    = "new_device"
	Throughput   = "throughput"
	UploadVolume = "upload_volume"
	Fanout       = "fanout"
	NewTunnel    = "new_tunnel"
	NoExport     = "no_export"
	APIDown      = "api_down"
	CoverageLow  = "coverage_low"
	RouterReboot = "router_reboot"
)

// Param describes one tunable number of a rule.
type Param struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
}

// Rule is one alert rule with its current settings.
type Rule struct {
	Type        string  `json:"type"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	PerDevice   bool    `json:"perDevice"`
	Enabled     bool    `json:"enabled"`
	Params      []Param `json:"params"`
	Muted       []int64 `json:"muted"` // device ids the rule ignores
}

func (r *Rule) param(key string) float64 {
	for _, p := range r.Params {
		if p.Key == key {
			return p.Value
		}
	}
	return 0
}

func (r *Rule) muted(dev int64) bool {
	for _, d := range r.Muted {
		if d == dev {
			return true
		}
	}
	return false
}

func defaults() []*Rule {
	return []*Rule{
		{Type: NewDevice, Title: "New device", PerDevice: true, Enabled: true,
			Description: "A device that has never been seen before sends traffic."},
		{Type: Throughput, Title: "Sustained throughput", PerDevice: true, Enabled: false,
			Description: "A device stays above a rate for several minutes in a row.",
			Params:      []Param{{"mbps", "Rate", "Mb/s", 100, 1, 10000}, {"minutes", "For", "min", 10, 2, 60}}},
		{Type: UploadVolume, Title: "Unusual upload", PerDevice: true, Enabled: true,
			Description: "A device uploads more than a set volume to the internet within a time window.",
			Params:      []Param{{"gb", "Volume", "GB", 10, 0.1, 10000}, {"hours", "Window", "h", 24, 1, 168}}},
		{Type: Fanout, Title: "Fan-out / scan pattern", PerDevice: true, Enabled: true,
			Description: "A device contacts an unusually large number of distinct hosts in a short time.",
			Params:      []Param{{"hosts", "Distinct hosts", "", 250, 10, 100000}, {"seconds", "Within", "s", 60, 10, 600}}},
		{Type: NewTunnel, Title: "New tunnel", PerDevice: true, Enabled: true,
			Description: "A device starts using a VPN or tunnel service it has not used before."},
		{Type: NoExport, Title: "Flow export stopped", Enabled: true,
			Description: "The router has not sent any flow records for a while.",
			Params:      []Param{{"minutes", "After", "min", 5, 1, 120}}},
		{Type: APIDown, Title: "Router API unreachable", Enabled: true,
			Description: "The collector cannot reach the router API, so live rates and names are stale.",
			Params:      []Param{{"minutes", "After", "min", 5, 1, 120}}},
		{Type: CoverageLow, Title: "Flow coverage low", Enabled: true,
			Description: "Flow records account for less of the WAN traffic than expected.",
			Params:      []Param{{"percent", "Below", "%", 85, 10, 99}, {"minutes", "For", "min", 10, 2, 120}}},
		{Type: RouterReboot, Title: "Router rebooted", Enabled: true,
			Description: "The router's boot time changed."},
	}
}

// DeviceRate is a device's lowest per-minute average rate over a window.
type DeviceRate struct {
	Dev    int64
	Name   string
	MinBps float64
}

// DeviceCount is a per-device counter (distinct hosts or bytes).
type DeviceCount struct {
	Dev   int64
	Name  string
	Count uint64
}

// Health is the collector's own state.
type Health struct {
	LastExport    time.Time
	APIConfigured bool
	APIUp         bool
	APIDownSince  time.Time
	Coverage      float64 // fraction, negative when unknown
	Started       time.Time
}

// Source supplies the measurements rules are checked against.
type Source interface {
	DeviceRates(minutes int) []DeviceRate
	DeviceFanout(window time.Duration) []DeviceCount
	DeviceUploads(since time.Time) []DeviceCount
	Health() Health
}

// Event is one alert event as shown in the UI.
type Event struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	RuleName string `json:"rule"`
	Dev      int64  `json:"device"`
	Severity string `json:"severity"` // info, warning, critical
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Started  int64  `json:"started"`
	Resolved int64  `json:"resolved"`
	Firing   bool   `json:"firing"`
	Acked    bool   `json:"acked"`
}

type firingKey struct{ typ, key string }

type firing struct {
	id       int64
	since    time.Time
	title    string
	severity string
}

// Manager owns the rules and the event log.
type Manager struct {
	st       *store.Store
	log      *slog.Logger
	appName  string
	mu       sync.Mutex
	rules    []*Rule
	firing   map[firingKey]*firing
	lastEnd  map[firingKey]time.Time // when a (type,key) last resolved, for the cooldown
	lowSince time.Time               // coverage below threshold since
	learnEnd time.Time               // new-device and new-tunnel events are suppressed until then
	client   *http.Client
}

const cooldown = 30 * time.Minute

// NewManager loads rules and open events from the store.
func NewManager(st *store.Store, appName string, log *slog.Logger) *Manager {
	m := &Manager{st: st, log: log, appName: appName, rules: defaults(), firing: map[firingKey]*firing{},
		lastEnd: map[firingKey]time.Time{}, client: &http.Client{Timeout: 10 * time.Second}}
	saved, _ := st.LoadRules()
	for _, s := range saved {
		r := m.rule(s.Type)
		if r == nil {
			continue
		}
		r.Enabled = s.Enabled
		var vals map[string]float64
		if json.Unmarshal([]byte(s.Params), &vals) == nil {
			for i := range r.Params {
				if v, ok := vals[r.Params[i].Key]; ok {
					r.Params[i].Value = v
				}
			}
		}
		_ = json.Unmarshal([]byte(s.Muted), &r.Muted)
	}
	open, _ := st.Events(0, true, 500)
	for _, e := range open {
		m.firing[firingKey{e.Type, e.Key}] = &firing{id: e.ID, since: time.Unix(e.Started, 0), title: e.Title, severity: e.Severity}
	}
	// A fresh install learns the network quietly before announcing "new" things.
	installed := st.Get("installed_at", "")
	if installed == "" {
		installed = time.Now().UTC().Format(time.RFC3339)
		_ = st.Set("installed_at", installed)
	}
	if t, err := time.Parse(time.RFC3339, installed); err == nil {
		m.learnEnd = t.Add(20 * time.Minute)
	}
	return m
}

func (m *Manager) rule(typ string) *Rule {
	for _, r := range m.rules {
		if r.Type == typ {
			return r
		}
	}
	return nil
}

// Rules returns a copy of the rules.
func (m *Manager) Rules() []Rule {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Rule, len(m.rules))
	for i, r := range m.rules {
		out[i] = *r
		out[i].Params = append([]Param{}, r.Params...) // never nil: the UI iterates it
		out[i].Muted = append([]int64{}, r.Muted...)
	}
	return out
}

// UpdateRule changes a rule's settings and persists them.
func (m *Manager) UpdateRule(typ string, enabled bool, params map[string]float64, muted []int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rule(typ)
	if r == nil {
		return fmt.Errorf("unknown rule %q", typ)
	}
	r.Enabled = enabled
	vals := map[string]float64{}
	for i := range r.Params {
		p := &r.Params[i]
		if v, ok := params[p.Key]; ok {
			p.Value = min(max(v, p.Min), p.Max)
		}
		vals[p.Key] = p.Value
	}
	if muted != nil {
		r.Muted = muted
	}
	pj, _ := json.Marshal(vals)
	mj, _ := json.Marshal(r.Muted)
	if r.Muted == nil {
		mj = []byte("[]")
	}
	return m.st.SaveRule(store.RuleRow{Type: typ, Enabled: enabled, Params: string(pj), Muted: string(mj)})
}

// Learning reports whether the quiet period after first install is still running.
func (m *Manager) Learning(now time.Time) bool { return now.Before(m.learnEnd) }

// DeviceSeen records the first appearance of a device.
func (m *Manager) DeviceSeen(now time.Time, dev int64, name, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.rule(NewDevice); r.Enabled && !m.Learning(now) {
		m.instant(now, NewDevice, dev, "info", "New device: "+name, detail)
	}
}

// TunnelSeen records a device using a tunnel service for the first time.
func (m *Manager) TunnelSeen(now time.Time, dev int64, name, service, remote string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.rule(NewTunnel); r.Enabled && !r.muted(dev) && !m.Learning(now) {
		m.instant(now, NewTunnel, dev, "warning", fmt.Sprintf("%s started using %s", name, service), "Remote endpoint: "+remote)
	}
}

// Rebooted records a router reboot.
func (m *Manager) Rebooted(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rule(RouterReboot).Enabled {
		m.instant(now, RouterReboot, 0, "warning", "Router rebooted", "The router's boot time changed; flow export restarted.")
	}
}

// Evaluate checks every stateful rule. Call it a few times a minute.
func (m *Manager) Evaluate(now time.Time, src Source) {
	// Measurements are gathered before taking the lock: the source takes its
	// own lock, and it calls back into the manager while holding it.
	m.mu.Lock()
	tp, uv, fo := *m.rule(Throughput), *m.rule(UploadVolume), *m.rule(Fanout)
	m.mu.Unlock()
	h := src.Health()
	var rates []DeviceRate
	var uploads, fanout []DeviceCount
	if tp.Enabled {
		rates = src.DeviceRates(int(tp.param("minutes")))
	}
	if uv.Enabled {
		uploads = src.DeviceUploads(now.Add(-time.Duration(uv.param("hours") * float64(time.Hour))))
	}
	if fo.Enabled {
		fanout = src.DeviceFanout(time.Duration(fo.param("seconds")) * time.Second)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	active := map[string]bool{}
	for _, d := range rates {
		if r := &tp; d.MinBps >= r.param("mbps")*1e6 && !r.muted(d.Dev) {
			key := fmt.Sprint(d.Dev)
			active[key] = true
			m.fire(now, Throughput, key, d.Dev, "warning", fmt.Sprintf("%s is sustaining high throughput", d.Name),
				fmt.Sprintf("At least %.0f Mb/s for %.0f minutes (threshold %.0f Mb/s).", d.MinBps/1e6, r.param("minutes"), r.param("mbps")))
		}
	}
	m.resolveExcept(now, Throughput, active)

	active = map[string]bool{}
	for _, d := range uploads {
		if r := &uv; float64(d.Count) >= r.param("gb")*1e9 && !r.muted(d.Dev) {
			key := fmt.Sprint(d.Dev)
			active[key] = true
			m.fire(now, UploadVolume, key, d.Dev, "warning", fmt.Sprintf("%s uploaded an unusual amount", d.Name),
				fmt.Sprintf("%.1f GB uploaded to the internet in the last %.0f hours (threshold %.1f GB).", float64(d.Count)/1e9, r.param("hours"), r.param("gb")))
		}
	}
	m.resolveExcept(now, UploadVolume, active)

	active = map[string]bool{}
	for _, d := range fanout {
		if r := &fo; float64(d.Count) >= r.param("hosts") && !r.muted(d.Dev) {
			key := fmt.Sprint(d.Dev)
			active[key] = true
			m.fire(now, Fanout, key, d.Dev, "critical", fmt.Sprintf("%s is contacting many hosts", d.Name),
				fmt.Sprintf("%d distinct hosts within %.0f seconds (threshold %.0f).", d.Count, r.param("seconds"), r.param("hosts")))
		}
	}
	m.resolveExcept(now, Fanout, active)

	settled := now.Sub(h.Started) > 3*time.Minute // give a fresh start time to receive its first export
	m.toggle(now, NoExport, func(r *Rule) (bool, string) {
		limit := time.Duration(r.param("minutes")) * time.Minute
		last := h.LastExport
		if last.IsZero() {
			last = h.Started
		}
		return settled && now.Sub(last) > limit, fmt.Sprintf("No flow records received since %s. Check /ip traffic-flow and its target on the router.", last.Format("15:04:05"))
	}, "critical", "Flow export stopped")

	m.toggle(now, APIDown, func(r *Rule) (bool, string) {
		limit := time.Duration(r.param("minutes")) * time.Minute
		return h.APIConfigured && !h.APIUp && !h.APIDownSince.IsZero() && now.Sub(h.APIDownSince) > limit,
			"Live rates, device names and DNS names are not updating. History from flow records continues."
	}, "warning", "Router API unreachable")

	m.toggle(now, CoverageLow, func(r *Rule) (bool, string) {
		if h.Coverage < 0 || h.Coverage*100 >= r.param("percent") {
			m.lowSince = time.Time{}
			return false, ""
		}
		if m.lowSince.IsZero() {
			m.lowSince = now
		}
		return now.Sub(m.lowSince) >= time.Duration(r.param("minutes"))*time.Minute,
			fmt.Sprintf("Flow records account for %.0f%% of WAN traffic (threshold %.0f%%). Records may be getting lost or the router's flow cache is full.", h.Coverage*100, r.param("percent"))
	}, "warning", "Flow coverage low")
}

func (m *Manager) toggle(now time.Time, typ string, check func(*Rule) (bool, string), severity, title string) {
	r := m.rule(typ)
	on, detail := false, ""
	if r.Enabled {
		on, detail = check(r)
	}
	if on {
		m.fire(now, typ, "", 0, severity, title, detail)
	} else {
		m.resolve(now, typ, "")
	}
}

func (m *Manager) fire(now time.Time, typ, key string, dev int64, severity, title, detail string) {
	k := firingKey{typ, key}
	if f := m.firing[k]; f != nil {
		_ = m.st.UpdateEvent(f.id, detail)
		return
	}
	if end, ok := m.lastEnd[k]; ok && now.Sub(end) < cooldown {
		return
	}
	id, err := m.st.InsertEvent(store.EventRow{Type: typ, Key: key, Dev: dev, Severity: severity, Title: title, Detail: detail, Started: now.Unix()})
	if err != nil {
		m.log.Error("alert: store event", "err", err)
		return
	}
	m.firing[k] = &firing{id: id, since: now, title: title, severity: severity}
	m.log.Info("alert firing", "type", typ, "title", title)
	go m.deliver("firing", typ, severity, title, detail, now)
}

func (m *Manager) resolve(now time.Time, typ, key string) {
	k := firingKey{typ, key}
	f := m.firing[k]
	if f == nil {
		return
	}
	delete(m.firing, k)
	m.lastEnd[k] = now
	_ = m.st.ResolveEvent(f.id, now.Unix())
	title := typ
	if r := m.rule(typ); r != nil {
		title = r.Title
	}
	go m.deliver("resolved", typ, "info", title+" resolved", "", now)
}

func (m *Manager) resolveExcept(now time.Time, typ string, keep map[string]bool) {
	var keys []string
	for k := range m.firing {
		if k.typ == typ && !keep[k.key] {
			keys = append(keys, k.key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		m.resolve(now, typ, key)
	}
}

func (m *Manager) instant(now time.Time, typ string, dev int64, severity, title, detail string) {
	if _, err := m.st.InsertEvent(store.EventRow{Type: typ, Key: fmt.Sprint(dev), Dev: dev, Severity: severity, Title: title, Detail: detail,
		Started: now.Unix(), Resolved: now.Unix()}); err != nil {
		m.log.Error("alert: store event", "err", err)
		return
	}
	m.log.Info("alert", "type", typ, "title", title)
	go m.deliver("info", typ, severity, title, detail, now)
}

// MostSevere describes the firing event that matters most (critical before
// warning, newest first) and how many are firing in total.
func (m *Manager) MostSevere() (title, severity string, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rank := map[string]int{"critical": 2, "warning": 1}
	var best *firing
	for _, f := range m.firing {
		if best == nil || rank[f.severity] > rank[best.severity] ||
			rank[f.severity] == rank[best.severity] && f.since.After(best.since) {
			best = f
		}
	}
	if best == nil {
		return "", "", 0
	}
	return best.title, best.severity, len(m.firing)
}

// FiringCount returns the number of events currently firing.
func (m *Manager) FiringCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.firing)
}

// Events lists events for the UI. dev 0 means every device.
func (m *Manager) Events(dev int64, limit int) []Event {
	rows, err := m.st.Events(dev, false, limit)
	if err != nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, 0, len(rows))
	for _, e := range rows {
		ev := Event{ID: e.ID, Type: e.Type, Dev: e.Dev, Severity: e.Severity, Title: e.Title, Detail: e.Detail,
			Started: e.Started, Resolved: e.Resolved, Firing: e.Resolved == 0, Acked: e.Acked}
		if r := m.rule(e.Type); r != nil {
			ev.RuleName = r.Title
		}
		out = append(out, ev)
	}
	return out
}

// WebhookURL returns the configured webhook.
func (m *Manager) WebhookURL() string { return m.st.Get("webhook_url", "") }

// SetWebhookURL stores the webhook.
func (m *Manager) SetWebhookURL(u string) error { return m.st.Set("webhook_url", u) }

// TestWebhook sends a test event and returns the delivery error, if any.
func (m *Manager) TestWebhook() error {
	return m.post(m.WebhookURL(), "info", "test", "info", "Test notification", "Webhook delivery from "+m.appName+" works.", time.Now())
}

func (m *Manager) deliver(state, typ, severity, title, detail string, at time.Time) {
	url := m.WebhookURL()
	if url == "" {
		return
	}
	if err := m.post(url, state, typ, severity, title, detail, at); err != nil {
		m.log.Warn("alert: webhook delivery failed", "err", err)
	}
}

func (m *Manager) post(url, state, typ, severity, title, detail string, at time.Time) error {
	if url == "" {
		return fmt.Errorf("no webhook URL configured")
	}
	text := title
	if detail != "" {
		text += " — " + detail
	}
	// "text" and "content" make the same payload readable by Slack- and
	// Discord-style webhooks; the other fields are for generic consumers.
	body, _ := json.Marshal(map[string]any{
		"app": m.appName, "state": state, "type": typ, "severity": severity, "title": title, "detail": detail,
		"time": at.UTC().Format(time.RFC3339), "text": text, "content": text,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Title", title) // ntfy
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}
