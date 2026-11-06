package collector

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	_ "modernc.org/sqlite"
	"sort"
	"time"
)

type Store struct {
	db        *sql.DB
	retention int
	max       int
}

func NewStore(path string, hours, max int) (*Store, error) {
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	s := &Store{db, hours, max}
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "CREATE TABLE IF NOT EXISTS events (seq INTEGER PRIMARY KEY, id TEXT UNIQUE, started_ms INTEGER, status INTEGER, duration_ms INTEGER, path TEXT, client TEXT, event TEXT)", "CREATE INDEX IF NOT EXISTS events_started ON events(started_ms)"} {
		if _, e = db.Exec(q); e != nil {
			db.Close()
			return nil, e
		}
	}
	if e := s.initTransport(); e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Insert(batch []Event) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	st, e := tx.Prepare("INSERT OR IGNORE INTO events(id,started_ms,status,duration_ms,path,client,event) VALUES(?,?,?,?,?,?,?)")
	if e != nil {
		return e
	}
	defer st.Close()
	for _, v := range batch {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		if _, e = st.Exec(v["id"], v["started_ms"], v["status"], v["duration_ms"], v["path"], v["client"], string(b)); e != nil {
			return e
		}
	}
	if _, e = tx.Exec("DELETE FROM events WHERE started_ms < ?", time.Now().Add(-time.Duration(s.retention)*time.Hour).UnixMilli()); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM events WHERE seq <= (SELECT seq FROM events ORDER BY seq DESC LIMIT 1 OFFSET ?)", s.max); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Recent() ([]json.RawMessage, error) {
	rows, e := s.db.Query("SELECT event FROM events ORDER BY seq DESC LIMIT 100")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, rows.Err()
}
func (s *Store) Get(id string) (json.RawMessage, error) {
	var raw string
	e := s.db.QueryRow("SELECT event FROM events WHERE id=?", id).Scan(&raw)
	return json.RawMessage(raw), e
}

type pair struct {
	key string
	n   int
}

func ranked(m map[string]int, max int) [][]any {
	p := []pair{}
	for k, n := range m {
		p = append(p, pair{k, n})
	}
	sort.Slice(p, func(i, j int) bool {
		if p[i].n == p[j].n {
			return p[i].key < p[j].key
		}
		return p[i].n > p[j].n
	})
	if len(p) > max {
		p = p[:max]
	}
	out := [][]any{}
	for _, v := range p {
		out = append(out, []any{v.key, v.n})
	}
	return out
}
func (s *Store) Summary() (map[string]any, error) {
	var total int
	if e := s.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&total); e != nil {
		return nil, e
	}
	rows, e := s.db.Query("SELECT duration_ms,path,status,client FROM events ORDER BY seq DESC LIMIT 10000")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	durations := []int64{}
	routes, clients, statuses := map[string]int{}, map[string]int{}, map[string]int{}
	errors := 0
	for rows.Next() {
		var d int64
		var path, client string
		var status int
		if e = rows.Scan(&d, &path, &status, &client); e != nil {
			return nil, e
		}
		durations = append(durations, d)
		routes[path]++
		clients[client]++
		statuses[fmt.Sprint(status)]++
		if status >= 500 {
			errors++
		}
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	percentile := func(p float64) any {
		if len(durations) == 0 {
			return nil
		}
		return durations[int(math.Ceil(float64(len(durations))*p))-1]
	}
	return map[string]any{"retained": total, "window": len(durations), "p50_ms": percentile(.5), "p95_ms": percentile(.95), "p99_ms": percentile(.99), "routes": ranked(routes, 20), "clients": ranked(clients, 10), "statuses": statuses, "errors": errors}, nil
}
