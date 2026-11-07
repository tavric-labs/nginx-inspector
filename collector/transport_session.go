package collector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// A short lease distinguishes an armed tracing helper from ordinary HTTP capture.
// The helper refreshes this only while its BPF process is alive.
type TraceSession struct {
	Active       bool  `json:"active"`
	Ready        bool  `json:"ready"`
	ExpiresMS    int64 `json:"expires_ms"`
	LeaseUntilMS int64 `json:"lease_until_ms"`
}

func (s *Server) traceSessionStatus(now time.Time) TraceSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.transportSession
	if now.UnixMilli() >= v.LeaseUntilMS || now.UnixMilli() >= v.ExpiresMS {
		v.Active = false
		v.Ready = false
	}
	return v
}
func (s *Server) updateTraceSession(raw []byte) (TraceSession, error) {
	var input struct {
		Active    bool  `json:"active"`
		Ready     bool  `json:"ready"`
		ExpiresMS int64 `json:"expires_ms"`
		MasterPID int   `json:"master_pid"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		return TraceSession{}, fmt.Errorf("invalid transport session")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return TraceSession{}, fmt.Errorf("invalid transport session")
	}
	if !s.workers.matchesMaster(input.MasterPID) {
		return TraceSession{}, fmt.Errorf("transport master scope mismatch")
	}
	now := time.Now()
	if input.Active && (!s.Control.Status().Active || input.ExpiresMS <= now.UnixMilli() || input.ExpiresMS > now.Add(2*time.Hour+time.Minute).UnixMilli()) {
		return TraceSession{}, fmt.Errorf("transport session requires active HTTP capture and bounded expiry")
	}
	v := TraceSession{}
	if input.Active {
		v = TraceSession{Active: true, Ready: input.Ready, ExpiresMS: input.ExpiresMS, LeaseUntilMS: now.Add(15 * time.Second).UnixMilli()}
	}
	s.mu.Lock()
	s.transportSession = v
	s.mu.Unlock()
	return v, nil
}
