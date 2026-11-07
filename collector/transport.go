package collector

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"
)

// Metadata only. Monotonic nanoseconds are decimal strings to preserve precision
// in browsers. Host PID + FD + generation + session prevents FD-reuse joins.
type TransportRecord struct {
	Schema        int                `json:"schema"`
	Kind          string             `json:"kind"`
	Session       string             `json:"session"`
	Master        int                `json:"master_pid"`
	HostPID       int                `json:"host_pid"`
	FD            int                `json:"fd"`
	Generation    string             `json:"generation"`
	At            string             `json:"at_ns"`
	Event         string             `json:"event"`
	RequestID     string             `json:"request_id,omitempty"`
	RequestNumber int64              `json:"request_number,omitempty"`
	Duration      string             `json:"duration_ns,omitempty"`
	Queued        string             `json:"queued_ns,omitempty"`
	Syn           string             `json:"syn_ns,omitempty"`
	Bytes         int64              `json:"bytes,omitempty"`
	Flags         uint32             `json:"flags,omitempty"`
	QueueSource   int                `json:"queue_source,omitempty"`
	Operation     string             `json:"operation,omitempty"`
	Errno         int                `json:"errno,omitempty"`
	CPU           *int               `json:"cpu,omitempty"`
	Socket        *SocketObservation `json:"socket,omitempty"`
	FromState     uint8              `json:"from_state,omitempty"`
	ToState       uint8              `json:"to_state,omitempty"`
	OnCPU         string             `json:"on_cpu_ns,omitempty"`
	OffCPU        string             `json:"off_cpu_ns,omitempty"`
	IRQ           string             `json:"irq_handler_ns,omitempty"`
	ResidenceMin  string             `json:"residence_min_ns,omitempty"`
	ResidenceMax  string             `json:"residence_max_ns,omitempty"`
	MatchedRanges uint8              `json:"matched_ranges,omitempty"`
	QueueOverruns uint64             `json:"queue_overruns,omitempty"`
}

// Socket snapshots describe one shared TCP connection, never a request's memory.
type SocketObservation struct {
	Point            string `json:"point"`
	State            uint8  `json:"state"`
	ReceiveMemory    uint64 `json:"receive_memory_bytes"`
	SendQueuedMemory uint64 `json:"send_queued_memory_bytes"`
	UnreadSequence   uint32 `json:"unread_sequence_units"`
	SmoothedRTT      uint32 `json:"srtt_us"`
	CongestionWindow uint32 `json:"cwnd_segments"`
	PacketsOut       uint32 `json:"packets_out"`
	Retransmits      uint32 `json:"total_retransmits"`
	SendWindow       uint32 `json:"send_window_bytes"`
	ReceiveWindow    uint32 `json:"receive_window_bytes"`
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
	if !contains([]string{"accept", "register", "ready", "read", "read_error", "write", "write_error", "socket", "network_rx", "buffer_residence", "tcp_state", "tcp_retransmit", "close", "request_begin", "request_end"}, t.Event) {
		return t, fmt.Errorf("unknown transport event")
	}
	if t.FromState > 32 || t.ToState > 32 || t.Event != "tcp_state" && (t.FromState != 0 || t.ToState != 0) {
		return t, fmt.Errorf("invalid TCP transition")
	}
	if t.Errno < 0 || t.Errno > 4095 || t.CPU != nil && (*t.CPU < 0 || *t.CPU > 65535) {
		return t, fmt.Errorf("invalid syscall/cpu metadata")
	}
	if t.Operation != "" && !contains([]string{"read", "readv", "recvfrom", "recvmsg", "write", "writev", "sendto", "sendmsg", "sendfile"}, t.Operation) {
		return t, fmt.Errorf("unknown syscall operation")
	}
	if t.CPU != nil && t.Event != "network_rx" {
		return t, fmt.Errorf("CPU attribution outside receive callback")
	}
	if t.Operation != "" {
		reading := t.Event == "read" || t.Event == "read_error"
		writing := t.Event == "write" || t.Event == "write_error"
		if !reading && !writing || reading && !contains([]string{"read", "readv", "recvfrom", "recvmsg"}, t.Operation) || writing && !contains([]string{"write", "writev", "sendto", "sendmsg", "sendfile"}, t.Operation) {
			return t, fmt.Errorf("operation/event mismatch")
		}
	}
	if (t.Event == "write" || t.Event == "write_error" || t.Event == "read_error") && t.Operation == "" {
		return t, fmt.Errorf("missing syscall operation")
	}
	failed := t.Event == "read_error" || t.Event == "write_error"
	if failed != (t.Errno > 0) || failed && t.Bytes != 0 {
		return t, fmt.Errorf("invalid error result")
	}
	if (failed || t.Event == "write" || t.Event == "network_rx") && t.Duration == "" {
		return t, fmt.Errorf("missing elapsed boundary")
	}
	if t.Event == "network_rx" && t.CPU == nil {
		return t, fmt.Errorf("missing receive CPU")
	}
	if t.Event == "socket" && t.Socket == nil {
		return t, fmt.Errorf("missing socket state")
	}
	if v := t.Socket; v != nil {
		if t.Event != "socket" || v.State > 32 || v.ReceiveMemory > 1<<40 || v.SendQueuedMemory > 1<<40 || !contains([]string{"recvmsg_return", "sendmsg_return"}, v.Point) {
			return t, fmt.Errorf("invalid socket snapshot")
		}
	}
	if t.OnCPU != "" || t.OffCPU != "" || t.IRQ != "" {
		if t.Event != "network_rx" || !decimalNS(t.OnCPU, false) || !decimalNS(t.OffCPU, false) || !decimalNS(t.IRQ, false) {
			return t, fmt.Errorf("invalid receive CPU accounting")
		}
		total, _ := strconv.ParseUint(t.Duration, 10, 64)
		on, _ := strconv.ParseUint(t.OnCPU, 10, 64)
		off, _ := strconv.ParseUint(t.OffCPU, 10, 64)
		irq, _ := strconv.ParseUint(t.IRQ, 10, 64)
		if on > total || off > total-on || irq > total-on-off {
			return t, fmt.Errorf("receive CPU accounting exceeds elapsed time")
		}
	}
	if t.Event == "buffer_residence" {
		if !decimalNS(t.ResidenceMin, false) || !decimalNS(t.ResidenceMax, false) || t.Bytes <= 0 || t.Bytes >= 1<<30 || t.MatchedRanges < 1 || t.MatchedRanges > 8 {
			return t, fmt.Errorf("invalid byte-residence bounds")
		}
		lo, _ := strconv.ParseUint(t.ResidenceMin, 10, 64)
		hi, _ := strconv.ParseUint(t.ResidenceMax, 10, 64)
		if lo > hi {
			return t, fmt.Errorf("reversed byte-residence bounds")
		}
	} else if t.ResidenceMin != "" || t.ResidenceMax != "" || t.MatchedRanges != 0 || t.QueueOverruns != 0 {
		return t, fmt.Errorf("byte-residence metadata on another event")
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
	if t.Event == "buffer_residence" {
		hi, _ := strconv.ParseUint(t.ResidenceMax, 10, 64)
		if hi > at-gen {
			return t, fmt.Errorf("byte residence predates observed socket")
		}
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
		"CREATE INDEX IF NOT EXISTS transport_markers ON transport_records(session,host_pid,fd,generation,request_id) WHERE event='request_begin'",
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
	var endSeq int64
	err := s.db.QueryRow("SELECT record,seq FROM transport_records WHERE request_id=? AND event='request_end' AND session=? AND host_pid=? AND fd=? AND generation=? ORDER BY seq LIMIT 1", id, m.Session, m.HostPID, m.FD, m.Generation).Scan(&endRaw, &endSeq)
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
	// Keep setup plus a bounded tail near the selected request's end marker.
	// Selection uses delivery order, not a guessed packet/request association.
	// Timestamps retain their actual values and all socket records remain shared.
	key := []any{m.Session, m.HostPID, m.FD, m.Generation}
	var total int
	if e = s.db.QueryRow("SELECT COUNT(*) FROM transport_records WHERE session=? AND host_pid=? AND fd=? AND generation=?", key...).Scan(&total); e != nil {
		return v, e
	}
	bySeq := map[int64]TransportRecord{}
	load := func(query string, args ...any) error {
		rows, e := s.db.Query(query, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			var seq int64
			if e = rows.Scan(&raw, &seq); e != nil {
				return e
			}
			var record TransportRecord
			if e = json.Unmarshal([]byte(raw), &record); e != nil {
				return e
			}
			bySeq[seq] = record
		}
		return rows.Err()
	}
	if e = load("SELECT record,seq FROM transport_records WHERE session=? AND host_pid=? AND fd=? AND generation=? ORDER BY seq LIMIT 32", key...); e != nil {
		return v, e
	}
	query := "SELECT record,seq FROM transport_records WHERE session=? AND host_pid=? AND fd=? AND generation=?"
	args := append([]any{}, key...)
	if v.End != nil {
		query += " AND seq<=?"
		args = append(args, endSeq+64)
	}
	query += " ORDER BY seq DESC LIMIT 224"
	if e = load(query, args...); e != nil {
		return v, e
	}
	seqs := make([]int64, 0, len(bySeq))
	for seq := range bySeq {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for _, seq := range seqs {
		record := bySeq[seq]
		v.Records = append(v.Records, record)
		if record.RequestID != "" && record.RequestID != id {
			v.Shared = true
		}
	}
	v.Truncated = total > len(v.Records)
	if !v.Shared {
		var shared int
		args := append(append([]any{}, key...), id)
		if e = s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM transport_records WHERE session=? AND host_pid=? AND fd=? AND generation=? AND event='request_begin' AND request_id<>?)", args...).Scan(&shared); e != nil {
			return v, e
		}
		v.Shared = shared != 0
	}

	v.Status = "correlated_connection"
	if m.RequestNumber > 1 {
		v.Shared = true
	}
	return v, nil
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
