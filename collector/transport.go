package collector

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"
)

// Metadata only. Monotonic nanoseconds are decimal strings to preserve precision
// in browsers. Host PID + FD + generation + session prevents FD-reuse joins.
type TransportRecord struct {
	Schema        int    `json:"schema"`
	Kind          string `json:"kind"`
	Session       string `json:"session"`
	Master        int    `json:"master_pid"`
	HostPID       int    `json:"host_pid"`
	FD            int    `json:"fd"`
	Generation    string `json:"generation"`
	At            string `json:"at_ns"`
	Event         string `json:"event"`
	RequestID     string `json:"request_id,omitempty"`
	RequestNumber int64  `json:"request_number,omitempty"`
	Duration      string `json:"duration_ns,omitempty"`
	Queued        string `json:"queued_ns,omitempty"`
	Syn           string `json:"syn_ns,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	Flags         uint32 `json:"flags,omitempty"`
	QueueSource   int    `json:"queue_source,omitempty"`
}

func decimalNS(s string, optional bool) bool {
	if s == "" {
		return optional
	}
	if len(s) > 1 && s[0] == '0' {
		return false
	}
	if len(s) > 20 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	_, e := strconv.ParseUint(s, 10, 64)
	return e == nil
}
func ValidateTransport(raw []byte) (TransportRecord, error) {
	var t TransportRecord
	if len(raw) > 1536 {
		return t, fmt.Errorf("transport payload too large")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&t); e != nil {
		return t, e
	}
	if d.Decode(new(any)) != io.EOF {
		return t, fmt.Errorf("trailing JSON")
	}
	if t.Schema != 1 || t.Kind != "transport" || len(t.Session) != 32 || t.Master <= 0 || t.HostPID <= 0 || t.FD < 0 || t.FD > 1<<24 || !decimalNS(t.Generation, false) || t.Generation == "0" || !decimalNS(t.At, false) || !decimalNS(t.Duration, true) || !decimalNS(t.Queued, true) || !decimalNS(t.Syn, true) || t.Bytes < 0 || t.RequestNumber < 0 || t.QueueSource < 0 || t.QueueSource > 2 {
		return t, fmt.Errorf("invalid transport bounds")
	}
	for _, c := range t.Session {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return t, fmt.Errorf("invalid session")
		}
	}
	if !contains([]string{"accept", "register", "ready", "read", "close", "request_begin", "request_end"}, t.Event) {
		return t, fmt.Errorf("unknown transport event")
	}
	marker := t.Event == "request_begin" || t.Event == "request_end"
	if marker {
		if !idPattern.MatchString(t.RequestID) || t.RequestID == "" || t.RequestNumber < 1 {
			return t, fmt.Errorf("missing request identity")
		}
	} else if t.RequestID != "" || t.RequestNumber != 0 {
		return t, fmt.Errorf("request attribution on connection event")
	}
	at, _ := strconv.ParseUint(t.At, 10, 64)
	gen, _ := strconv.ParseUint(t.Generation, 10, 64)
	if at < gen {
		return t, fmt.Errorf("event predates connection generation")
	}
	for _, v := range []string{t.Queued, t.Syn} {
		if v != "" {
			n, _ := strconv.ParseUint(v, 10, 64)
			if n > at {
				return t, fmt.Errorf("future queue boundary")
			}
		}
	}
	return t, nil
}
func (s *Store) initTransport() error {
	for _, q := range []string{
		"CREATE TABLE IF NOT EXISTS transport_records (seq INTEGER PRIMARY KEY, session TEXT NOT NULL, host_pid INTEGER NOT NULL, fd INTEGER NOT NULL, generation TEXT NOT NULL, request_id TEXT NOT NULL, at_ns TEXT NOT NULL, event TEXT NOT NULL, created_ms INTEGER NOT NULL, record TEXT NOT NULL)",
		"CREATE INDEX IF NOT EXISTS transport_request ON transport_records(request_id)",
		"CREATE INDEX IF NOT EXISTS transport_connection ON transport_records(session,host_pid,fd,generation)",
		"CREATE INDEX IF NOT EXISTS transport_created ON transport_records(created_ms)",
	} {
		if _, e := s.db.Exec(q); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) insertTransport(batch []TransportRecord) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	st, e := tx.Prepare("INSERT INTO transport_records(session,host_pid,fd,generation,request_id,at_ns,event,created_ms,record) VALUES(?,?,?,?,?,?,?,?,?)")
	if e != nil {
		return e
	}
	defer st.Close()
	for _, v := range batch {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		if _, e = st.Exec(v.Session, v.HostPID, v.FD, v.Generation, v.RequestID, v.At, v.Event, time.Now().UnixMilli(), string(b)); e != nil {
			return e
		}
	}
	if _, e = tx.Exec("DELETE FROM transport_records WHERE created_ms < ?", time.Now().Add(-time.Duration(s.retention)*time.Hour).UnixMilli()); e != nil {
		return e
	}
	max := min(max(s.max*32, 32), 320000)
	if _, e = tx.Exec("DELETE FROM transport_records WHERE seq <= (SELECT seq FROM transport_records ORDER BY seq DESC LIMIT 1 OFFSET ?)", max); e != nil {
		return e
	}
	return tx.Commit()
}

type TransportView struct {
	RequestID string            `json:"request_id"`
	Status    string            `json:"status"`
	Shared    bool              `json:"connection_shared"`
	Truncated bool              `json:"truncated"`
	Begin     *TransportRecord  `json:"begin,omitempty"`
	End       *TransportRecord  `json:"end,omitempty"`
	Records   []TransportRecord `json:"records"`
}

func (s *Store) Transport(id string) (TransportView, error) {
	v := TransportView{RequestID: id, Status: "not_captured", Records: []TransportRecord{}}
	rows, e := s.db.Query("SELECT record FROM transport_records WHERE seq IN (SELECT MIN(seq) FROM transport_records WHERE request_id=? AND event='request_begin' GROUP BY session,host_pid,fd,generation LIMIT 2)", id)
	if e != nil {
		return v, e
	}
	var markers []TransportRecord
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			rows.Close()
			return v, e
		}
		var t TransportRecord
		if e = json.Unmarshal([]byte(raw), &t); e != nil {
			rows.Close()
			return v, e
		}
		markers = append(markers, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return v, e
	}
	if len(markers) == 0 {
		return v, nil
	}
	m := markers[0]
	for _, x := range markers[1:] {
		if x.Session != m.Session || x.HostPID != m.HostPID || x.FD != m.FD || x.Generation != m.Generation {
			v.Status = "ambiguous"
			return v, nil
		}
	}
	v.Begin = &m
	var endRaw string
	err := s.db.QueryRow("SELECT record FROM transport_records WHERE request_id=? AND event='request_end' AND session=? AND host_pid=? AND fd=? AND generation=? ORDER BY seq LIMIT 1", id, m.Session, m.HostPID, m.FD, m.Generation).Scan(&endRaw)
	if err != nil && err != sql.ErrNoRows {
		return v, err
	}
	if err == nil {
		var end TransportRecord
		if e = json.Unmarshal([]byte(endRaw), &end); e != nil {
			return v, e
		}
		v.End = &end
	}
	// Return connection history, explicitly shared across requests/streams. Do not
	// call all socket activity within an HTTP lifetime that request's own work.
	rows, e = s.db.Query("SELECT record FROM transport_records WHERE session=? AND host_pid=? AND fd=? AND generation=? ORDER BY seq LIMIT 257", m.Session, m.HostPID, m.FD, m.Generation)
	if e != nil {
		return v, e
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return v, e
		}
		var t TransportRecord
		if e = json.Unmarshal([]byte(raw), &t); e != nil {
			return v, e
		}
		if len(v.Records) == 256 {
			v.Truncated = true
			break
		}
		v.Records = append(v.Records, t)
		if t.RequestID != "" && t.RequestID != id {
			v.Shared = true
		}
	}
	v.Status = "correlated_connection"
	if m.RequestNumber > 1 {
		v.Shared = true
	}
	return v, rows.Err()
}
func (s *Server) persistTransport() {
	for first := range s.transportPending {
		batch := []TransportRecord{first}
	drain:
		for len(batch) < 200 {
			select {
			case t, ok := <-s.transportPending:
				if !ok {
					break drain
				}
				batch = append(batch, t)
			default:
				break drain
			}
		}
		if e := s.Store.insertTransport(batch); e != nil {
			s.inc("transport_storage_dropped", uint64(len(batch)))
		} else {
			s.inc("transport_persisted", uint64(len(batch)))
		}
	}
}
