// mikrotik-home-netflow-plus receives IPFIX flow export from a MikroTik
// router, optionally polls the router API for live rates and names, and serves
// a web interface.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"mikrotik-home-netflow-plus/internal/alert"
	"mikrotik-home-netflow-plus/internal/api"
	"mikrotik-home-netflow-plus/internal/config"
	"mikrotik-home-netflow-plus/internal/engine"
	"mikrotik-home-netflow-plus/internal/enrich"
	"mikrotik-home-netflow-plus/internal/flow"
	"mikrotik-home-netflow-plus/internal/ipfix"
	"mikrotik-home-netflow-plus/internal/routeros"
	"mikrotik-home-netflow-plus/internal/store"
	"mikrotik-home-netflow-plus/web"
)

const appName = "mikrotik-home-netflow-plus"

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(healthcheck())
		case "replay":
			os.Exit(replay(os.Args[2:]))
		case "version":
			fmt.Println(appName, version)
			return
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, appName+":", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	log.Info("starting", "app", appName, "version", version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Claim both ports before anything else starts, so a port that is already
	// taken is reported once, clearly, instead of half-starting.
	httpLn, err := net.Listen("tcp", cfg.HTTPListen)
	if err != nil {
		return fmt.Errorf("web interface cannot listen on %s: %w (set NFP_HTTP_LISTEN to a free port)", cfg.HTTPListen, err)
	}
	defer httpLn.Close()

	st, err := store.Open(filepath.Join(cfg.DataDir, "netflow.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	// Without explicit local networks the private ranges are assumed until the
	// router API reports the real ones. The CGNAT range is deliberately absent:
	// it is what a WAN address behind carrier-grade NAT looks like.
	local := cfg.LocalNets
	if len(local) == 0 {
		for _, p := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7", "fe80::/10"} {
			local = append(local, netip.MustParsePrefix(p))
		}
	}
	exporters := cfg.Exporters
	if len(exporters) == 0 && cfg.RouterAddr != "" {
		if host, _, err := net.SplitHostPort(cfg.RouterAddr); err == nil {
			if a, err := netip.ParseAddr(host); err == nil {
				exporters = append(exporters, a.Unmap())
			}
		}
	}
	topo := flow.NewTopology(local, cfg.Sites, exporters)

	asn := openASN(cfg, log)
	names := enrich.NewNames(100_000)
	alerts := alert.NewManager(st, appName, log)
	eng, err := engine.New(engine.Options{
		FoldBelow:     cfg.FoldBelow,
		FlushInterval: cfg.FlushInterval,
		Retention:     store.Retention{Conns: cfg.RetentionConns, Rollup1m: cfg.Retention1m, Rollup1h: cfg.Retention1h, MaxBytes: cfg.DBMaxBytes},
		StaticTopo:    len(cfg.LocalNets) > 0,
		WANInterfaces: cfg.WANInterfaces,
		APIConfigured: cfg.RouterAddr != "",
		Exporters:     exporters,
		FlowPort:      cfg.FlowPort(),
		BuildID:       buildID(),
	}, st, topo, names, asn, alerts, log)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	dec := ipfix.NewDecoder(filepath.Join(cfg.DataDir, "templates.json"))
	norm := flow.NewNormalizer(topo, cfg.ActiveFlowTimeout, cfg.FlowPort())

	// Flow listener.
	udpAddr, err := net.ResolveUDPAddr("udp", cfg.FlowListen)
	if err != nil {
		return fmt.Errorf("NFP_FLOW_LISTEN: %w", err)
	}
	udp, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen for flows on %s: %w", cfg.FlowListen, err)
	}
	defer udp.Close()
	_ = udp.SetReadBuffer(8 << 20)
	var packets, rejected atomic.Uint64
	if len(exporters) == 0 {
		log.Warn("no exporter addresses configured: accepting flow records from any source (set NFP_EXPORTERS)")
	}
	go receive(ctx, udp, exporters, dec, norm, eng, alerts, &packets, &rejected, log)
	log.Info("listening for IPFIX", "addr", cfg.FlowListen, "exporters", exporters)

	// Router API lane.
	pinFile := filepath.Join(cfg.DataDir, "router-cert.pin")
	if cfg.RouterAddr != "" {
		opts := routeros.Options{Addr: cfg.RouterAddr, User: cfg.RouterUser, Password: cfg.RouterPassword, TLS: cfg.RouterTLS,
			Fingerprint: cfg.RouterFingerprint, Pin: pinner(pinFile, log)}
		go routeros.NewPoller(opts, routeros.DefaultIntervals(), eng, log).Run(ctx)
	} else {
		log.Info("router API not configured: running from flow records only (live views are 15-75 s behind)")
	}

	go eng.Run(ctx)

	exporterNames := make([]string, len(exporters))
	for i, a := range exporters {
		exporterNames[i] = a.String()
	}
	srv := api.New(eng, st, alerts, dec, norm, api.Info{
		App: appName, Version: version, FlowListen: cfg.FlowListen, FlowPort: cfg.FlowPort(), Exporters: exporterNames,
		RouterAddr: cfg.RouterAddr, RouterTLS: cfg.RouterTLS,
		Fingerprint: func() string {
			if cfg.RouterFingerprint != "" {
				return cfg.RouterFingerprint
			}
			raw, _ := os.ReadFile(pinFile)
			return strings.TrimSpace(string(raw))
		},
		Retention:       map[string]string{"connections": cfg.RetentionConns.String(), "minute": cfg.Retention1m.String(), "hour": cfg.Retention1h.String()},
		FoldBelow:       cfg.FoldBelow,
		DBMaxBytes:      cfg.DBMaxBytes,
		UDPStats:        func() (uint64, uint64) { return packets.Load(), rejected.Load() },
		ActiveTimeoutS:  int(cfg.ActiveFlowTimeout.Seconds()),
		AttributionNote: asnAttribution(asn),
		BuildID:         buildID(),
	}, web.Dist(), cfg.AuthPassword, log)
	go srv.Run(ctx)

	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(httpLn) }()
	log.Info("web interface ready", "addr", cfg.HTTPListen, "login", cfg.AuthPassword != "")

	select {
	case err := <-errc:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdown)
	eng.Flush(time.Now())
	return nil
}

// receive reads flow export datagrams until ctx is cancelled.
func receive(ctx context.Context, udp *net.UDPConn, exporters []netip.Addr, dec *ipfix.Decoder, norm *flow.Normalizer,
	eng *engine.Engine, alerts *alert.Manager, packets, rejected *atomic.Uint64, log *slog.Logger) {
	context.AfterFunc(ctx, func() { udp.Close() })
	allowed := map[netip.Addr]bool{}
	for _, a := range exporters {
		allowed[a] = true
	}
	buf := make([]byte, 65535)
	var lastWarn time.Time
	for {
		n, from, err := udp.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		recv := time.Now()
		src := from.Addr().Unmap()
		if len(allowed) > 0 && !allowed[src] {
			rejected.Add(1)
			if recv.Sub(lastWarn) > time.Minute {
				lastWarn = recv
				log.Warn("dropping flow packet from an address that is not a configured exporter", "from", src)
			}
			continue
		}
		packets.Add(1)
		msg, err := dec.Decode(src, buf[:n])
		if err != nil {
			log.Debug("undecodable flow packet", "from", src, "err", err)
			continue
		}
		flows, rebooted := norm.Normalize(src, recv, msg)
		if rebooted {
			alerts.Rebooted(recv)
		}
		eng.AddFlows(recv, flows)
	}
}

// pinner implements trust on first use for the router's API certificate.
func pinner(path string, log *slog.Logger) func(string) error {
	return func(fp string) error {
		raw, err := os.ReadFile(path)
		pinned := strings.TrimSpace(string(raw))
		if err != nil || pinned == "" {
			log.Info("pinning router API certificate on first use", "sha256", fp)
			return os.WriteFile(path, []byte(fp+"\n"), 0o600)
		}
		if pinned != fp {
			return fmt.Errorf("router API certificate changed (pinned %s, got %s); delete %s or set NFP_ROUTER_CERT_FINGERPRINT if this is expected", pinned, fp, path)
		}
		return nil
	}
}

func openASN(cfg *config.Config, log *slog.Logger) *enrich.ASN {
	candidates := []string{cfg.ASNDatabase, filepath.Join(cfg.DataDir, "asn.mmdb"), "/usr/share/nfp/asn.mmdb"}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		db, err := enrich.OpenASN(p)
		if err != nil {
			log.Warn("cannot open ASN database", "path", p, "err", err)
			continue
		}
		log.Info("organisation labels enabled", "database", p)
		return db
	}
	log.Info("no ASN database found: destinations without a DNS name show as bare addresses")
	return nil
}

func asnAttribution(asn *enrich.ASN) string {
	kind, _ := asn.Describe()
	switch {
	case strings.Contains(strings.ToLower(kind), "dbip") || strings.Contains(strings.ToLower(kind), "db-ip"):
		return "IP to ASN data by DB-IP (https://db-ip.com), CC BY 4.0"
	case strings.Contains(strings.ToLower(kind), "ipinfo"):
		return "IP address data powered by IPinfo (https://ipinfo.io)"
	case kind != "":
		return "Organisation labels from " + kind
	}
	return ""
}

// buildID fingerprints the embedded web interface. index.html names the
// hashed script and style files, so it changes whenever the interface does.
func buildID() string {
	raw, err := fs.ReadFile(web.Dist(), "index.html")
	if err != nil {
		return version
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:6])
}

// healthcheck is used by the container HEALTHCHECK: the image has no shell or curl.
func healthcheck() int {
	addr := os.Getenv("NFP_HTTP_LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// replay sends a capture file to a collector with its original timing. It is
// a development aid: replay <capture.bin> [host:port] [speed].
func replay(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: replay <capture.bin> [host:port] [speed]")
		return 2
	}
	target, speed := "127.0.0.1:2055", 1.0
	if len(args) > 1 {
		target = args[1]
	}
	if len(args) > 2 {
		fmt.Sscanf(args[2], "%f", &speed)
	}
	dgrams, err := ipfix.ReadCapture(args[0])
	if err != nil || len(dgrams) == 0 {
		fmt.Fprintln(os.Stderr, "replay:", err)
		return 1
	}
	conn, err := net.Dial("udp", target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		return 1
	}
	defer conn.Close()
	// No timestamps need rewriting: the collector anchors flow times on its own
	// clock, so replayed flows land at "now".
	start := time.Now()
	for _, d := range dgrams {
		wait := time.Duration(float64(d.Received.Sub(dgrams[0].Received))/speed) - time.Since(start)
		if wait > 0 {
			time.Sleep(wait)
		}
		if _, err := conn.Write(d.Payload); err != nil {
			fmt.Fprintln(os.Stderr, "replay:", err)
			return 1
		}
	}
	fmt.Printf("replayed %d datagrams to %s\n", len(dgrams), target)
	return 0
}
