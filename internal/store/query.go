package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// Filter selects rollup rows.
type Filter struct {
	From, To int64   // Unix seconds, [From, To)
	Zones    []uint8 // empty means every zone
	Dev      int64   // 0 means every device
	Dest     int64
	Svc      int64
	Hourly   bool // read the hourly table instead of the minute table
}

func (f Filter) table() string {
	if f.Hourly {
		return "rollup_1h"
	}
	return "rollup_1m"
}

func (f Filter) where() (string, []any) {
	w := []string{"ts >= ?", "ts < ?"}
	args := []any{f.From, f.To}
	if len(f.Zones) > 0 {
		w = append(w, "zone IN ("+placeholders(len(f.Zones))+")")
		for _, z := range f.Zones {
			args = append(args, z)
		}
	}
	for _, c := range []struct {
		col string
		v   int64
	}{{"dev", f.Dev}, {"dest", f.Dest}, {"svc", f.Svc}} {
		if c.v != 0 {
			w = append(w, c.col+" = ?")
			args = append(args, c.v)
		}
	}
	return strings.Join(w, " AND "), args
}

// Point is one bucket of a time series, in bytes.
type Point struct {
	TS       int64
	Down, Up uint64
}

// Series returns byte totals per step-second bucket.
func (s *Store) Series(f Filter, step int64) ([]Point, error) {
	where, args := f.where()
	q := fmt.Sprintf("SELECT (ts/%d)*%d AS b, SUM(down), SUM(up) FROM %s WHERE %s GROUP BY b ORDER BY b", step, step, f.table(), where)
	return s.points(q, args...)
}

// WanSeries returns WAN interface byte counts per step-second bucket.
func (s *Store) WanSeries(from, to, step int64) ([]Point, error) {
	q := fmt.Sprintf("SELECT (ts/%d)*%d AS b, SUM(rx), SUM(tx) FROM wan_series WHERE ts >= ? AND ts < ? GROUP BY b ORDER BY b", step, step)
	return s.points(q, from, to)
}

func (s *Store) points(q string, args ...any) ([]Point, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.TS, &p.Down, &p.Up); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// TopRow is one entry of a ranking.
type TopRow struct {
	ID              int64
	Down, Up, Conns uint64
}

// Top ranks devices ("dev"), destinations ("dest") or services ("svc") by bytes.
func (s *Store) Top(f Filter, by string, limit int) ([]TopRow, error) {
	if by != "dev" && by != "dest" && by != "svc" {
		return nil, fmt.Errorf("store: bad ranking %q", by)
	}
	where, args := f.where()
	q := fmt.Sprintf("SELECT %s, SUM(down), SUM(up), SUM(conns) FROM %s WHERE %s GROUP BY %s ORDER BY SUM(down)+SUM(up) DESC LIMIT ?", by, f.table(), where, by)
	rows, err := s.db.Query(q, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopRow
	for rows.Next() {
		var r TopRow
		if err := rows.Scan(&r.ID, &r.Down, &r.Up, &r.Conns); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Totals sums the rows matched by the filter.
func (s *Store) Totals(f Filter) (TopRow, error) {
	where, args := f.where()
	var down, up, conns sql.NullInt64
	err := s.db.QueryRow("SELECT SUM(down), SUM(up), SUM(conns) FROM "+f.table()+" WHERE "+where, args...).Scan(&down, &up, &conns)
	return TopRow{Down: uint64(down.Int64), Up: uint64(up.Int64), Conns: uint64(conns.Int64)}, err
}

// DevicePoint is one bucket of a per-device series.
type DevicePoint struct {
	Dev int64
	Point
}

// DeviceSeries returns a series per device, for sparklines.
func (s *Store) DeviceSeries(f Filter, step int64) ([]DevicePoint, error) {
	where, args := f.where()
	q := fmt.Sprintf("SELECT dev, (ts/%d)*%d AS b, SUM(down), SUM(up) FROM %s WHERE %s GROUP BY dev, b ORDER BY dev, b", step, step, f.table(), where)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DevicePoint
	for rows.Next() {
		var p DevicePoint
		if err := rows.Scan(&p.Dev, &p.TS, &p.Down, &p.Up); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SankeyRow is the traffic of one device/service/destination combination.
type SankeyRow struct {
	Dev, Svc, Dest int64
	Down, Up       uint64
}

// Sankey returns the heaviest device/service/destination combinations.
func (s *Store) Sankey(f Filter, limit int) ([]SankeyRow, error) {
	where, args := f.where()
	q := "SELECT dev, svc, dest, SUM(down), SUM(up) FROM " + f.table() + " WHERE " + where +
		" GROUP BY dev, svc, dest ORDER BY SUM(down)+SUM(up) DESC LIMIT ?"
	rows, err := s.db.Query(q, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SankeyRow
	for rows.Next() {
		var r SankeyRow
		if err := rows.Scan(&r.Dev, &r.Svc, &r.Dest, &r.Down, &r.Up); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ConnFilter selects stored connections.
type ConnFilter struct {
	From, To int64
	Zones    []uint8
	Dev      int64
	Dest     int64
	Svc      int64
	Text     string // matches remote name or either address
	Port     int
	Sort     string // "bytes" (default), "time"
	Limit    int
	Offset   int
}

// Conns returns stored connections that were active inside the time range.
func (s *Store) Conns(f ConnFilter) ([]ConnRow, error) {
	w := []string{"last_ts >= ?", "first_ts < ?"}
	args := []any{f.From, f.To}
	if len(f.Zones) > 0 {
		w = append(w, "zone IN ("+placeholders(len(f.Zones))+")")
		for _, z := range f.Zones {
			args = append(args, z)
		}
	}
	for _, c := range []struct {
		col string
		v   int64
	}{{"dev", f.Dev}, {"dest", f.Dest}, {"svc", f.Svc}} {
		if c.v != 0 {
			w = append(w, c.col+" = ?")
			args = append(args, c.v)
		}
	}
	if f.Port > 0 {
		w = append(w, "(remote_port = ? OR local_port = ?)")
		args = append(args, f.Port, f.Port)
	}
	if f.Text != "" {
		like := "%" + strings.NewReplacer("%", "", "_", "").Replace(f.Text) + "%"
		w = append(w, "(remote_name LIKE ? OR remote_ip LIKE ? OR local_ip LIKE ?)")
		args = append(args, like, like, like)
	}
	order := "up+down DESC"
	if f.Sort == "time" {
		order = "last_ts DESC"
	}
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	q := `SELECT id,first_ts,last_ts,dev,local_ip,local_port,remote_ip,remote_port,proto,svc,dest,zone,remote_name,up,down,up_pk,down_pk
		FROM conns WHERE ` + strings.Join(w, " AND ") + " ORDER BY " + order + " LIMIT ? OFFSET ?"
	rows, err := s.db.Query(q, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnRow
	for rows.Next() {
		var c ConnRow
		if err := rows.Scan(&c.ID, &c.FirstTS, &c.LastTS, &c.Dev, &c.LocalIP, &c.LocalPort, &c.RemoteIP, &c.RemotePort, &c.Proto,
			&c.Svc, &c.Dest, &c.Zone, &c.RemoteName, &c.Up, &c.Down, &c.UpPk, &c.DownPk); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- alerts ----

// RuleRow mirrors the alert_rules table.
type RuleRow struct {
	Type    string
	Enabled bool
	Params  string // JSON object
	Muted   string // JSON array of device ids
}

// LoadRules returns stored rule overrides.
func (s *Store) LoadRules() ([]RuleRow, error) {
	rows, err := s.db.Query("SELECT type,enabled,params,muted FROM alert_rules")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuleRow
	for rows.Next() {
		var r RuleRow
		if err := rows.Scan(&r.Type, &r.Enabled, &r.Params, &r.Muted); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveRule stores a rule.
func (s *Store) SaveRule(r RuleRow) error {
	_, err := s.db.Exec(`INSERT INTO alert_rules(type,enabled,params,muted) VALUES(?,?,?,?)
		ON CONFLICT(type) DO UPDATE SET enabled=excluded.enabled, params=excluded.params, muted=excluded.muted`,
		r.Type, r.Enabled, r.Params, r.Muted)
	return err
}

// EventRow mirrors the alert_events table.
type EventRow struct {
	ID       int64
	Type     string
	Key      string
	Dev      int64
	Severity string
	Title    string
	Detail   string
	Started  int64
	Resolved int64
	Acked    bool
}

// InsertEvent stores a new event and returns its id.
func (s *Store) InsertEvent(e EventRow) (int64, error) {
	res, err := s.db.Exec("INSERT INTO alert_events(type,key,dev,severity,title,detail,started,resolved) VALUES(?,?,?,?,?,?,?,?)",
		e.Type, e.Key, e.Dev, e.Severity, e.Title, e.Detail, e.Started, e.Resolved)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateEvent refreshes the detail text of a firing event.
func (s *Store) UpdateEvent(id int64, detail string) error {
	_, err := s.db.Exec("UPDATE alert_events SET detail=? WHERE id=?", detail, id)
	return err
}

// ResolveEvent marks an event resolved.
func (s *Store) ResolveEvent(id, at int64) error {
	_, err := s.db.Exec("UPDATE alert_events SET resolved=? WHERE id=? AND resolved=0", at, id)
	return err
}

// AckEvents marks events as seen. id 0 acknowledges everything.
func (s *Store) AckEvents(id int64) error {
	if id == 0 {
		_, err := s.db.Exec("UPDATE alert_events SET acked=1 WHERE acked=0")
		return err
	}
	_, err := s.db.Exec("UPDATE alert_events SET acked=1 WHERE id=?", id)
	return err
}

// Events lists events, newest first. dev 0 means every device.
func (s *Store) Events(dev int64, onlyFiring bool, limit int) ([]EventRow, error) {
	w, args := []string{"1=1"}, []any{}
	if dev != 0 {
		w = append(w, "dev = ?")
		args = append(args, dev)
	}
	if onlyFiring {
		w = append(w, "resolved = 0")
	}
	rows, err := s.db.Query("SELECT id,type,key,dev,severity,title,detail,started,resolved,acked FROM alert_events WHERE "+
		strings.Join(w, " AND ")+" ORDER BY started DESC, id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.ID, &e.Type, &e.Key, &e.Dev, &e.Severity, &e.Title, &e.Detail, &e.Started, &e.Resolved, &e.Acked); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UnackedCount returns the number of events nobody has looked at yet.
func (s *Store) UnackedCount() int {
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM alert_events WHERE acked=0").Scan(&n)
	return n
}
