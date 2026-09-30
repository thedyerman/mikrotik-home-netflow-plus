// Package store persists devices, rollups, connections and alert state in a
// single SQLite file.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS devices(
  did INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, mac TEXT NOT NULL DEFAULT '',
  hostname TEXT NOT NULL DEFAULT '', comment TEXT NOT NULL DEFAULT '', custom_name TEXT NOT NULL DEFAULT '',
  vendor TEXT NOT NULL DEFAULT '', ips TEXT NOT NULL DEFAULT '', router INTEGER NOT NULL DEFAULT 0,
  first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS dests(
  id INTEGER PRIMARY KEY, key TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, label TEXT NOT NULL, asn INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS services(
  id INTEGER PRIMARY KEY, label TEXT NOT NULL UNIQUE, tunnel INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS rollup_1m(
  ts INTEGER NOT NULL, dev INTEGER NOT NULL, dest INTEGER NOT NULL, svc INTEGER NOT NULL, zone INTEGER NOT NULL,
  up INTEGER NOT NULL, down INTEGER NOT NULL, up_pk INTEGER NOT NULL, down_pk INTEGER NOT NULL, conns INTEGER NOT NULL,
  PRIMARY KEY(ts, dev, dest, svc, zone)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS rollup_1h(
  ts INTEGER NOT NULL, dev INTEGER NOT NULL, dest INTEGER NOT NULL, svc INTEGER NOT NULL, zone INTEGER NOT NULL,
  up INTEGER NOT NULL, down INTEGER NOT NULL, up_pk INTEGER NOT NULL, down_pk INTEGER NOT NULL, conns INTEGER NOT NULL,
  PRIMARY KEY(ts, dev, dest, svc, zone)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS rollup_1m_dev ON rollup_1m(dev, ts);
CREATE INDEX IF NOT EXISTS rollup_1h_dev ON rollup_1h(dev, ts);
CREATE TABLE IF NOT EXISTS conns(
  id INTEGER PRIMARY KEY, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, dev INTEGER NOT NULL,
  local_ip TEXT NOT NULL, local_port INTEGER NOT NULL, remote_ip TEXT NOT NULL, remote_port INTEGER NOT NULL,
  proto INTEGER NOT NULL, svc INTEGER NOT NULL, dest INTEGER NOT NULL, zone INTEGER NOT NULL,
  remote_name TEXT NOT NULL DEFAULT '', up INTEGER NOT NULL, down INTEGER NOT NULL,
  up_pk INTEGER NOT NULL, down_pk INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS conns_last ON conns(last_ts);
CREATE INDEX IF NOT EXISTS conns_dev ON conns(dev, last_ts);
CREATE TABLE IF NOT EXISTS wan_series(ts INTEGER PRIMARY KEY, rx INTEGER NOT NULL, tx INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS dns_names(ip TEXT PRIMARY KEY, name TEXT NOT NULL, seen INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS device_tunnels(dev INTEGER NOT NULL, svc INTEGER NOT NULL, first_seen INTEGER NOT NULL,
  PRIMARY KEY(dev, svc)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS alert_rules(type TEXT PRIMARY KEY, enabled INTEGER NOT NULL, params TEXT NOT NULL, muted TEXT NOT NULL DEFAULT '[]') WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS alert_events(
  id INTEGER PRIMARY KEY, type TEXT NOT NULL, key TEXT NOT NULL, dev INTEGER NOT NULL DEFAULT 0,
  severity TEXT NOT NULL, title TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '',
  started INTEGER NOT NULL, resolved INTEGER NOT NULL DEFAULT 0, acked INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS alert_events_started ON alert_events(started);
CREATE TABLE IF NOT EXISTS kv(k TEXT PRIMARY KEY, v TEXT NOT NULL) WITHOUT ROWID;
`

// Store wraps the database.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (and if needed creates) the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	fresh := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fresh = true
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(0)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if fresh { // must be set before the first table exists
		if _, err := db.Exec("PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
			return nil, err
		}
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("store: schema: %w", err)
	}
	return &Store{db: db, path: path}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SizeBytes returns the on-disk size of the database including its WAL.
func (s *Store) SizeBytes() int64 {
	var n int64
	for _, p := range []string{s.path, s.path + "-wal"} {
		if st, err := os.Stat(p); err == nil {
			n += st.Size()
		}
	}
	return n
}

// ---- key/value ----

// Get returns a stored setting, or def when it is missing.
func (s *Store) Get(k, def string) string {
	var v string
	if err := s.db.QueryRow("SELECT v FROM kv WHERE k=?", k).Scan(&v); err != nil {
		return def
	}
	return v
}

// Set stores a setting.
func (s *Store) Set(k, v string) error {
	_, err := s.db.Exec("INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", k, v)
	return err
}

// ---- dictionaries ----

// DeviceRow mirrors the devices table.
type DeviceRow struct {
	DID        int64
	ID         string
	MAC        string
	Hostname   string
	Comment    string
	CustomName string
	Vendor     string
	IPs        string // comma separated, most recent first
	Router     bool
	FirstSeen  int64
	LastSeen   int64
}

// DestRow mirrors the dests table.
type DestRow struct {
	ID    int64
	Key   string
	Kind  string // domain, org, ip, site, local
	Label string
	ASN   uint32
}

// ServiceRow mirrors the services table.
type ServiceRow struct {
	ID     int64
	Label  string
	Tunnel bool
}

// LoadDevices returns every known device.
func (s *Store) LoadDevices() ([]DeviceRow, error) {
	rows, err := s.db.Query("SELECT did,id,mac,hostname,comment,custom_name,vendor,ips,router,first_seen,last_seen FROM devices")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceRow
	for rows.Next() {
		var d DeviceRow
		if err := rows.Scan(&d.DID, &d.ID, &d.MAC, &d.Hostname, &d.Comment, &d.CustomName, &d.Vendor, &d.IPs, &d.Router, &d.FirstSeen, &d.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// LoadDests returns the destination dictionary.
func (s *Store) LoadDests() ([]DestRow, error) {
	rows, err := s.db.Query("SELECT id,key,kind,label,asn FROM dests")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DestRow
	for rows.Next() {
		var d DestRow
		if err := rows.Scan(&d.ID, &d.Key, &d.Kind, &d.Label, &d.ASN); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// LoadServices returns the service dictionary.
func (s *Store) LoadServices() ([]ServiceRow, error) {
	rows, err := s.db.Query("SELECT id,label,tunnel FROM services")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceRow
	for rows.Next() {
		var d ServiceRow
		if err := rows.Scan(&d.ID, &d.Label, &d.Tunnel); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MaxConnID returns the highest connection row id in use.
func (s *Store) MaxConnID() int64 {
	var n sql.NullInt64
	_ = s.db.QueryRow("SELECT MAX(id) FROM conns").Scan(&n)
	return n.Int64
}

// DNSName is a remembered IP to name mapping.
type DNSName struct {
	IP   string
	Name string
	Seen int64
}

// LoadDNSNames returns the most recently seen name mappings.
func (s *Store) LoadDNSNames(limit int) ([]DNSName, error) {
	rows, err := s.db.Query("SELECT ip,name,seen FROM dns_names ORDER BY seen DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DNSName
	for rows.Next() {
		var d DNSName
		if err := rows.Scan(&d.IP, &d.Name, &d.Seen); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TunnelSeen is one (device, tunnel service) pair already observed.
type TunnelSeen struct{ Dev, Svc int64 }

// LoadDeviceTunnels returns the tunnel services each device has used before.
func (s *Store) LoadDeviceTunnels() ([]TunnelSeen, error) {
	rows, err := s.db.Query("SELECT dev,svc FROM device_tunnels")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TunnelSeen
	for rows.Next() {
		var t TunnelSeen
		if err := rows.Scan(&t.Dev, &t.Svc); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---- batched writes ----

// RollupKey identifies one rollup cell.
type RollupKey struct {
	TS   int64 // start of the minute, Unix seconds
	Dev  int64
	Dest int64
	Svc  int64
	Zone uint8
}

// RollupVal is the additive content of a rollup cell.
type RollupVal struct {
	Up, Down, UpPk, DownPk, Conns uint64
}

// ConnRow mirrors the conns table.
type ConnRow struct {
	ID         int64
	FirstTS    int64
	LastTS     int64
	Dev        int64
	LocalIP    string
	LocalPort  uint16
	RemoteIP   string
	RemotePort uint16
	Proto      uint8
	Svc        int64
	Dest       int64
	Zone       uint8
	RemoteName string
	Up, Down   uint64
	UpPk       uint64
	DownPk     uint64
}

// WanSample is the byte count of all WAN interfaces in one 10-second bucket.
type WanSample struct {
	TS     int64
	Rx, Tx uint64
}

// Batch is everything the engine writes in one flush.
type Batch struct {
	Devices  []DeviceRow
	Dests    []DestRow
	Services []ServiceRow
	Merges   [][2]int64 // device ids: move everything from [0] into [1]
	Rollups  map[RollupKey]RollupVal
	Conns    []ConnRow
	Wan      []WanSample
	DNS      []DNSName
	Tunnels  []TunnelSeen
	Now      int64
}

// Write applies a batch in one transaction.
func (s *Store) Write(b *Batch) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, d := range b.Devices {
		if _, err := tx.Exec(`INSERT INTO devices(did,id,mac,hostname,comment,custom_name,vendor,ips,router,first_seen,last_seen)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(did) DO UPDATE SET id=excluded.id, mac=excluded.mac, hostname=excluded.hostname, comment=excluded.comment,
			custom_name=excluded.custom_name, vendor=excluded.vendor, ips=excluded.ips, router=excluded.router,
			first_seen=excluded.first_seen, last_seen=excluded.last_seen`,
			d.DID, d.ID, d.MAC, d.Hostname, d.Comment, d.CustomName, d.Vendor, d.IPs, d.Router, d.FirstSeen, d.LastSeen); err != nil {
			return fmt.Errorf("devices: %w", err)
		}
	}
	for _, d := range b.Dests {
		if _, err := tx.Exec("INSERT OR IGNORE INTO dests(id,key,kind,label,asn) VALUES(?,?,?,?,?)", d.ID, d.Key, d.Kind, d.Label, d.ASN); err != nil {
			return fmt.Errorf("dests: %w", err)
		}
	}
	for _, d := range b.Services {
		if _, err := tx.Exec("INSERT OR IGNORE INTO services(id,label,tunnel) VALUES(?,?,?)", d.ID, d.Label, d.Tunnel); err != nil {
			return fmt.Errorf("services: %w", err)
		}
	}
	for _, m := range b.Merges {
		if err := mergeDevice(tx, m[0], m[1]); err != nil {
			return fmt.Errorf("merge: %w", err)
		}
	}
	if len(b.Rollups) > 0 {
		for _, table := range []string{"rollup_1m", "rollup_1h"} {
			st, err := tx.Prepare("INSERT INTO " + table + `(ts,dev,dest,svc,zone,up,down,up_pk,down_pk,conns) VALUES(?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(ts,dev,dest,svc,zone) DO UPDATE SET up=up+excluded.up, down=down+excluded.down,
				up_pk=up_pk+excluded.up_pk, down_pk=down_pk+excluded.down_pk, conns=conns+excluded.conns`)
			if err != nil {
				return err
			}
			for k, v := range b.Rollups {
				ts := k.TS
				if table == "rollup_1h" {
					ts -= ts % 3600
				}
				if _, err := st.Exec(ts, k.Dev, k.Dest, k.Svc, k.Zone, v.Up, v.Down, v.UpPk, v.DownPk, v.Conns); err != nil {
					st.Close()
					return fmt.Errorf("%s: %w", table, err)
				}
			}
			st.Close()
		}
	}
	for _, c := range b.Conns {
		if _, err := tx.Exec(`INSERT INTO conns(id,first_ts,last_ts,dev,local_ip,local_port,remote_ip,remote_port,proto,svc,dest,zone,remote_name,up,down,up_pk,down_pk)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET last_ts=excluded.last_ts, dev=excluded.dev, dest=excluded.dest, remote_name=excluded.remote_name,
			up=excluded.up, down=excluded.down, up_pk=excluded.up_pk, down_pk=excluded.down_pk`,
			c.ID, c.FirstTS, c.LastTS, c.Dev, c.LocalIP, c.LocalPort, c.RemoteIP, c.RemotePort, c.Proto, c.Svc, c.Dest, c.Zone, c.RemoteName,
			c.Up, c.Down, c.UpPk, c.DownPk); err != nil {
			return fmt.Errorf("conns: %w", err)
		}
	}
	for _, w := range b.Wan {
		if _, err := tx.Exec("INSERT INTO wan_series(ts,rx,tx) VALUES(?,?,?) ON CONFLICT(ts) DO UPDATE SET rx=rx+excluded.rx, tx=tx+excluded.tx", w.TS, w.Rx, w.Tx); err != nil {
			return fmt.Errorf("wan_series: %w", err)
		}
	}
	for _, d := range b.DNS {
		if _, err := tx.Exec("INSERT INTO dns_names(ip,name,seen) VALUES(?,?,?) ON CONFLICT(ip) DO UPDATE SET name=excluded.name, seen=excluded.seen", d.IP, d.Name, d.Seen); err != nil {
			return fmt.Errorf("dns_names: %w", err)
		}
	}
	for _, t := range b.Tunnels {
		if _, err := tx.Exec("INSERT OR IGNORE INTO device_tunnels(dev,svc,first_seen) VALUES(?,?,?)", t.Dev, t.Svc, b.Now); err != nil {
			return fmt.Errorf("device_tunnels: %w", err)
		}
	}
	return tx.Commit()
}

// mergeDevice folds everything recorded for device `from` into device `to`.
// It happens when a device first seen only by IP is later identified by MAC.
func mergeDevice(tx *sql.Tx, from, to int64) error {
	for _, table := range []string{"rollup_1m", "rollup_1h"} {
		if _, err := tx.Exec("INSERT INTO "+table+`(ts,dev,dest,svc,zone,up,down,up_pk,down_pk,conns)
			SELECT ts,?,dest,svc,zone,up,down,up_pk,down_pk,conns FROM `+table+` WHERE dev=?
			ON CONFLICT(ts,dev,dest,svc,zone) DO UPDATE SET up=up+excluded.up, down=down+excluded.down,
			up_pk=up_pk+excluded.up_pk, down_pk=down_pk+excluded.down_pk, conns=conns+excluded.conns`, to, from); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE dev=?", from); err != nil {
			return err
		}
	}
	for _, q := range []string{
		"UPDATE conns SET dev=? WHERE dev=?",
		"UPDATE alert_events SET dev=? WHERE dev=?",
		"INSERT OR IGNORE INTO device_tunnels(dev,svc,first_seen) SELECT ?,svc,first_seen FROM device_tunnels WHERE dev=?",
	} {
		if _, err := tx.Exec(q, to, from); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("DELETE FROM device_tunnels WHERE dev=?", from); err != nil {
		return err
	}
	_, err := tx.Exec("DELETE FROM devices WHERE did=?", from)
	return err
}

// SetDeviceName stores a user-chosen device name.
func (s *Store) SetDeviceName(did int64, name string) error {
	_, err := s.db.Exec("UPDATE devices SET custom_name=? WHERE did=?", name, did)
	return err
}

// ---- retention ----

// Retention says how long each tier is kept.
type Retention struct {
	Conns, Rollup1m, Rollup1h time.Duration
	MaxBytes                  int64
}

// Prune deletes data older than the retention and, if the database is still
// above its size cap, keeps dropping the oldest day of fine-grained data.
func (s *Store) Prune(ctx context.Context, r Retention, now time.Time) error {
	cut := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	stmts := []struct {
		q   string
		arg int64
	}{
		{"DELETE FROM conns WHERE last_ts < ?", cut(r.Conns)},
		{"DELETE FROM rollup_1m WHERE ts < ?", cut(r.Rollup1m)},
		{"DELETE FROM rollup_1h WHERE ts < ?", cut(r.Rollup1h)},
		{"DELETE FROM wan_series WHERE ts < ?", cut(r.Rollup1m)},
		{"DELETE FROM alert_events WHERE started < ? AND resolved > 0", cut(r.Rollup1h)},
		{"DELETE FROM dns_names WHERE seen < ?", cut(r.Rollup1h)},
	}
	for _, st := range stmts {
		if _, err := s.db.ExecContext(ctx, st.q, st.arg); err != nil {
			return err
		}
	}
	// Drop dictionary entries nothing refers to any more.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM dests WHERE id NOT IN (SELECT DISTINCT dest FROM rollup_1h)
		AND id NOT IN (SELECT DISTINCT dest FROM rollup_1m) AND id NOT IN (SELECT DISTINCT dest FROM conns)`); err != nil {
		return err
	}
	for i := 0; r.MaxBytes > 0 && s.SizeBytes() > r.MaxBytes && i < 60; i++ {
		var oldest sql.NullInt64
		if err := s.db.QueryRowContext(ctx, "SELECT MIN(ts) FROM rollup_1m").Scan(&oldest); err != nil || !oldest.Valid {
			break
		}
		limit := oldest.Int64 + 86400
		if limit > now.Unix()-3600 {
			break // never eat into the last hour
		}
		for _, q := range []string{"DELETE FROM rollup_1m WHERE ts < ?", "DELETE FROM conns WHERE last_ts < ?", "DELETE FROM wan_series WHERE ts < ?"} {
			if _, err := s.db.ExecContext(ctx, q, limit); err != nil {
				return err
			}
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA incremental_vacuum"); err != nil {
			return err
		}
		_, _ = s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	}
	_, err := s.db.ExecContext(ctx, "PRAGMA incremental_vacuum(2000)")
	return err
}

// TableCounts returns row counts for the status page.
func (s *Store) TableCounts() map[string]int64 {
	out := map[string]int64{}
	for _, t := range []string{"devices", "dests", "rollup_1m", "rollup_1h", "conns", "dns_names", "alert_events"} {
		var n int64
		if s.db.QueryRow("SELECT COUNT(*) FROM "+t).Scan(&n) == nil {
			out[t] = n
		}
	}
	return out
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
