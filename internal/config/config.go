// Package config reads settings from NFP_* environment variables.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"mikrotik-home-netflow-plus/internal/flow"
)

// Config is the complete runtime configuration.
type Config struct {
	FlowListen string
	HTTPListen string
	DataDir    string
	LogLevel   string

	Exporters []netip.Addr // empty: accept flow records from any source

	LocalNets     []netip.Prefix // empty: discover from the router API, else private ranges
	Sites         []flow.Site
	WANInterfaces []string

	RouterAddr        string // host or host:port; empty disables the API lane
	RouterUser        string
	RouterPassword    string
	RouterTLS         bool
	RouterFingerprint string

	ActiveFlowTimeout time.Duration

	RetentionConns time.Duration
	Retention1m    time.Duration
	Retention1h    time.Duration
	FoldBelow      uint64
	DBMaxBytes     int64

	ASNDatabase  string
	AuthPassword string
}

// Load reads the environment.
func Load() (*Config, error) {
	c := &Config{
		FlowListen:        env("NFP_FLOW_LISTEN", ":2055"),
		HTTPListen:        env("NFP_HTTP_LISTEN", ":8080"),
		DataDir:           env("NFP_DATA_DIR", "/data"),
		LogLevel:          env("NFP_LOG_LEVEL", "info"),
		RouterAddr:        env("NFP_ROUTER_ADDR", ""),
		RouterUser:        env("NFP_ROUTER_USER", ""),
		RouterFingerprint: env("NFP_ROUTER_CERT_FINGERPRINT", ""),
		ASNDatabase:       env("NFP_ASN_DB", ""),
	}
	var err error
	fail := func(name string, e error) error { return fmt.Errorf("%s: %w", name, e) }

	for _, s := range list("NFP_EXPORTERS") {
		a, e := netip.ParseAddr(s)
		if e != nil {
			return nil, fail("NFP_EXPORTERS", e)
		}
		c.Exporters = append(c.Exporters, a.Unmap())
	}
	for _, s := range list("NFP_LOCAL_NETS") {
		p, e := netip.ParsePrefix(s)
		if e != nil {
			return nil, fail("NFP_LOCAL_NETS", e)
		}
		c.LocalNets = append(c.LocalNets, p.Masked())
	}
	for _, s := range list("NFP_SITES") {
		name, cidr, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fail("NFP_SITES", fmt.Errorf("%q is not name=cidr", s))
		}
		p, e := netip.ParsePrefix(strings.TrimSpace(cidr))
		if e != nil {
			return nil, fail("NFP_SITES", e)
		}
		c.Sites = append(c.Sites, flow.Site{Name: strings.TrimSpace(name), Prefix: p.Masked()})
	}
	c.WANInterfaces = list("NFP_WAN_INTERFACES")

	if c.RouterPassword, err = secret("NFP_ROUTER_PASSWORD"); err != nil {
		return nil, err
	}
	if c.AuthPassword, err = secret("NFP_AUTH_PASSWORD"); err != nil {
		return nil, err
	}
	c.RouterTLS = env("NFP_ROUTER_TLS", "true") != "false" && env("NFP_ROUTER_TLS", "true") != "0"
	if c.RouterAddr != "" {
		if !strings.Contains(c.RouterAddr, ":") || strings.Count(c.RouterAddr, ":") > 1 && !strings.Contains(c.RouterAddr, "]") {
			port := "8728"
			if c.RouterTLS {
				port = "8729"
			}
			c.RouterAddr = joinHostPort(c.RouterAddr, port)
		}
		if c.RouterUser == "" {
			return nil, fmt.Errorf("NFP_ROUTER_ADDR is set but NFP_ROUTER_USER is not")
		}
	}

	if c.ActiveFlowTimeout, err = duration("NFP_ACTIVE_FLOW_TIMEOUT", time.Minute); err != nil {
		return nil, err
	}
	if c.RetentionConns, err = duration("NFP_RETENTION_CONNECTIONS", 48*time.Hour); err != nil {
		return nil, err
	}
	if c.Retention1m, err = duration("NFP_RETENTION_1M", 7*24*time.Hour); err != nil {
		return nil, err
	}
	if c.Retention1h, err = duration("NFP_RETENTION_1H", 30*24*time.Hour); err != nil {
		return nil, err
	}
	fold, err := size("NFP_FOLD_BELOW", 10_000)
	if err != nil {
		return nil, err
	}
	c.FoldBelow = uint64(fold)
	if c.DBMaxBytes, err = size("NFP_DB_MAX_SIZE", 2<<30); err != nil {
		return nil, err
	}
	return c, nil
}

// FlowPort returns the UDP port flow records arrive on.
func (c *Config) FlowPort() uint16 {
	_, port, _ := strings.Cut(c.FlowListen[strings.LastIndexByte(c.FlowListen, ':'):], ":")
	n, _ := strconv.ParseUint(port, 10, 16)
	return uint16(n)
}

func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

func env(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func list(name string) []string {
	var out []string
	for _, s := range strings.Split(os.Getenv(name), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// secret reads NAME, or the file named by NAME_FILE.
func secret(name string) (string, error) {
	if v := os.Getenv(name); v != "" {
		return v, nil
	}
	path := os.Getenv(name + "_FILE")
	if path == "" {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s_FILE: %w", name, err)
	}
	return strings.TrimRight(string(raw), "\r\n"), nil
}

// duration parses Go durations plus a "d" suffix for days.
func duration(name string, def time.Duration) (time.Duration, error) {
	v := env(name, "")
	if v == "" {
		return def, nil
	}
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return d, nil
}

// size parses byte sizes such as "10KB", "2GB" or a plain number of bytes.
func size(name string, def int64) (int64, error) {
	v := strings.ToUpper(strings.ReplaceAll(env(name, ""), " ", ""))
	if v == "" {
		return def, nil
	}
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		m      int64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}} {
		if s, ok := strings.CutSuffix(v, u.suffix); ok {
			v, mult = s, u.m
			break
		}
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return int64(n * float64(mult)), nil
}
