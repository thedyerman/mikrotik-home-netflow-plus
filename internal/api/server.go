// Package api serves the JSON API, the live WebSocket feed and the embedded
// web interface.
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"mikrotik-home-netflow-plus/internal/alert"
	"mikrotik-home-netflow-plus/internal/engine"
	"mikrotik-home-netflow-plus/internal/flow"
	"mikrotik-home-netflow-plus/internal/ipfix"
	"mikrotik-home-netflow-plus/internal/store"
)

// Info is static information shown on the status page.
type Info struct {
	App             string
	Version         string
	FlowListen      string
	FlowPort        uint16
	Exporters       []string
	RouterAddr      string
	RouterTLS       bool
	Fingerprint     func() string // pinned router certificate fingerprint, if any
	Retention       map[string]string
	FoldBelow       uint64
	DBMaxBytes      int64
	UDPStats        func() (packets, rejected uint64)
	ActiveTimeoutS  int
	AttributionNote string
	BuildID         string
}

// Server is the HTTP front end.
type Server struct {
	eng      *engine.Engine
	st       *store.Store
	alerts   *alert.Manager
	dec      *ipfix.Decoder
	norm     *flow.Normalizer
	info     Info
	static   fs.FS
	password string
	log      *slog.Logger

	mu       sync.Mutex
	sessions map[string]time.Time
	clients  map[*client]struct{}
}

type client struct {
	ch chan []byte
}

// New creates the server. static is the built web interface.
func New(eng *engine.Engine, st *store.Store, alerts *alert.Manager, dec *ipfix.Decoder, norm *flow.Normalizer,
	info Info, static fs.FS, password string, log *slog.Logger) *Server {
	return &Server{eng: eng, st: st, alerts: alerts, dec: dec, norm: norm, info: info, static: static, password: password,
		log: log, sessions: map[string]time.Time{}, clients: map[*client]struct{}{}}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /api/v1/session", s.handleSession)
	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/logout", s.handleLogout)

	api := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.auth(h)) }
	api("GET /api/v1/live", s.handleLive)
	api("GET /api/v1/live/series", s.handleLiveSeries)
	api("GET /api/v1/live/flows", s.handleLiveFlows)
	api("GET /api/v1/status", s.handleStatus)
	api("GET /api/v1/today", s.handleToday)
	api("GET /api/v1/overview", s.handleOverview)
	api("GET /api/v1/devices", s.handleDevices)
	api("GET /api/v1/devices/{id}", s.handleDevice)
	api("PATCH /api/v1/devices/{id}", s.handleRenameDevice)
	api("GET /api/v1/sankey", s.handleSankey)
	api("GET /api/v1/flows", s.handleFlows)
	api("GET /api/v1/alerts/events", s.handleEvents)
	api("POST /api/v1/alerts/ack", s.handleAck)
	api("GET /api/v1/alerts/rules", s.handleRules)
	api("PUT /api/v1/alerts/rules/{type}", s.handleUpdateRule)
	api("GET /api/v1/settings", s.handleSettings)
	api("PUT /api/v1/settings", s.handleUpdateSettings)
	api("POST /api/v1/settings/test-webhook", s.handleTestWebhook)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { fail(w, http.StatusNotFound, "not found") })
	mux.HandleFunc("GET /dashboard", s.handleDashboard)
	mux.Handle("/", s.staticHandler())
	return mux
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func intParam(r *http.Request, name string, def int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get(name)); err == nil {
		return n
	}
	return def
}

// ---- authentication ----

const sessionCookie = "nfp_session"

func (s *Server) authed(r *http.Request) bool {
	if s.password == "" {
		return true
	}
	if s.basicOK(r) {
		return true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[c.Value]
	return ok && time.Now().Before(exp)
}

// basicOK accepts the web password as HTTP basic credentials (any user
// name). Unattended displays cannot fill in the login form, but kiosk
// players can answer a basic-auth prompt with a stored credential.
func (s *Server) basicOK(r *http.Request) bool {
	_, pass, ok := r.BasicAuth()
	return ok && s.password != "" && subtle.ConstantTimeCompare([]byte(pass), []byte(s.password)) == 1
}

// startSession issues a session cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token := hex.EncodeToString(raw)
	exp := time.Now().Add(30 * 24 * time.Hour)
	s.mu.Lock()
	for t, e := range s.sessions {
		if time.Now().After(e) {
			delete(s.sessions, t)
		}
	}
	s.sessions[token] = exp
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: exp, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
}

// handleDashboard serves the wall-display page. When a web password is set
// it asks for it with HTTP basic auth instead of the login form, then hands
// out a normal session so the page's own requests are authorised.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if s.password != "" {
		switch {
		case s.basicOK(r):
			s.startSession(w, r)
		case !s.authed(r):
			w.Header().Set("WWW-Authenticate", `Basic realm="`+s.info.App+`", charset="UTF-8"`)
			http.Error(w, "password required", http.StatusUnauthorized)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.static, "index.html")
}

func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			fail(w, http.StatusUnauthorized, "login required")
			return
		}
		// State-changing requests must come from the page itself.
		if r.Method != http.MethodGet && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			fail(w, http.StatusForbidden, "cross-site request refused")
			return
		}
		next(w, r)
	})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"authRequired": s.password != "", "authenticated": s.authed(r), "app": s.info.App, "version": s.info.Version, "build": s.info.BuildID})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if s.password == "" || subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.password)) != 1 {
		time.Sleep(time.Second) // slow down guessing
		fail(w, http.StatusUnauthorized, "wrong password")
		return
	}
	s.startSession(w, r)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- live ----

// Run pushes a tick to every connected browser once a second.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			n := len(s.clients)
			s.mu.Unlock()
			if n == 0 {
				continue
			}
			msg, err := json.Marshal(s.eng.Tick())
			if err != nil {
				continue
			}
			s.mu.Lock()
			for c := range s.clients {
				select {
				case c.ch <- msg:
				default: // slow client: drop this tick rather than queue
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	// Same-origin by default; localhost origins are allowed for the dev server.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"localhost:*", "127.0.0.1:*"}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	c := &client{ch: make(chan []byte, 2)}
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}()
	ctx := conn.CloseRead(r.Context())
	if first, err := json.Marshal(s.eng.Tick()); err == nil {
		c.ch <- first
	}
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-c.ch:
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) handleLiveSeries(w http.ResponseWriter, r *http.Request) {
	series, mode := s.eng.LiveSeries(int64(min(max(intParam(r, "seconds", 600), 30), 1190)))
	writeJSON(w, map[string]any{"series": series, "mode": mode})
}

func (s *Server) handleLiveFlows(w http.ResponseWriter, r *http.Request) {
	rows, mode, total := s.eng.LiveRows(min(max(intParam(r, "limit", 500), 1), 2000))
	if rows == nil {
		rows = []engine.LiveRow{}
	}
	writeJSON(w, map[string]any{"rows": rows, "mode": mode, "total": total, "ts": time.Now().UnixMilli()})
}

// ---- history ----

func (s *Server) handleToday(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	t, err := s.eng.Today(time.Unix(since, 0))
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, t)
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	o, err := s.eng.Overview(q.Get("range"), q.Get("zone"), 0)
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, o)
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	list, err := s.eng.Devices(r.URL.Query().Get("range"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"devices": list})
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, err := s.eng.Device(id, r.URL.Query().Get("range"))
	if err != nil {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, d)
}

func (s *Server) handleRenameDevice(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Name) > 80 {
		fail(w, http.StatusBadRequest, "name is too long")
		return
	}
	if err := s.eng.RenameDevice(id, body.Name); err != nil {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleSankey(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dev, _ := strconv.ParseInt(q.Get("device"), 10, 64)
	d, err := s.eng.Sankey(q.Get("range"), q.Get("zone"), dev)
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, d)
}

func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ConnFilter{Text: strings.TrimSpace(q.Get("q")), Port: intParam(r, "port", 0), Sort: q.Get("sort"),
		Limit: intParam(r, "limit", 200), Offset: max(intParam(r, "offset", 0), 0)}
	f.Dev, _ = strconv.ParseInt(q.Get("device"), 10, 64)
	f.Dest, _ = strconv.ParseInt(q.Get("dest"), 10, 64)
	f.Svc, _ = strconv.ParseInt(q.Get("service"), 10, 64)
	rows, err := s.eng.Flows(q.Get("range"), q.Get("zone"), f)
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"rows": rows, "foldBelow": s.info.FoldBelow})
}

// ---- alerts and settings ----

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	dev, _ := strconv.ParseInt(r.URL.Query().Get("device"), 10, 64)
	ev := s.alerts.Events(dev, min(max(intParam(r, "limit", 200), 1), 1000))
	if ev == nil {
		ev = []alert.Event{}
	}
	writeJSON(w, map[string]any{"events": ev, "firing": s.alerts.FiringCount(), "unacked": s.st.UnackedCount()})
}

func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID int64 `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.st.AckEvents(body.ID); err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleRules(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"rules": s.alerts.Rules()})
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool               `json:"enabled"`
		Params  map[string]float64 `json:"params"`
		Muted   []int64            `json:"muted"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.alerts.UpdateRule(r.PathValue("type"), body.Enabled, body.Params, body.Muted); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"rules": s.alerts.Rules()})
}

func (s *Server) handleSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"webhookUrl": s.alerts.WebhookURL()})
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WebhookURL string `json:"webhookUrl"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	u := strings.TrimSpace(body.WebhookURL)
	if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		fail(w, http.StatusBadRequest, "the webhook must be an http:// or https:// URL")
		return
	}
	if err := s.alerts.SetWebhookURL(u); err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"webhookUrl": u})
}

func (s *Server) handleTestWebhook(w http.ResponseWriter, _ *http.Request) {
	if err := s.alerts.TestWebhook(); err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- status ----

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	packets, rejected := s.info.UDPStats()
	collector := ""
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		collector, _, _ = net.SplitHostPort(a.String())
	}
	fp := ""
	if s.info.Fingerprint != nil {
		fp = s.info.Fingerprint()
	}
	writeJSON(w, map[string]any{
		"app": s.info.App, "version": s.info.Version, "now": time.Now().Unix(),
		"engine":      s.eng.Status(),
		"decoder":     s.dec.Stats(),
		"clocks":      s.norm.Clocks(),
		"udp":         map[string]any{"listen": s.info.FlowListen, "packets": packets, "rejected": rejected, "exporters": s.info.Exporters},
		"store":       map[string]any{"bytes": s.st.SizeBytes(), "maxBytes": s.info.DBMaxBytes, "tables": s.st.TableCounts(), "retention": s.info.Retention, "foldBelow": s.info.FoldBelow},
		"routerApi":   map[string]any{"addr": s.info.RouterAddr, "tls": s.info.RouterTLS, "fingerprint": fp},
		"setup":       map[string]any{"collector": collector, "flowPort": s.info.FlowPort, "activeTimeoutSeconds": s.info.ActiveTimeoutS},
		"attribution": s.info.AttributionNote,
	})
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "err", err)
	fail(w, http.StatusInternalServerError, "internal error")
}

// ---- static files ----

func (s *Server) staticHandler() http.Handler {
	files := http.FileServerFS(s.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(s.static, path); errors.Is(err, fs.ErrNotExist) {
			// Client-side routes all resolve to the application shell.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			path = "index.html"
		}
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
