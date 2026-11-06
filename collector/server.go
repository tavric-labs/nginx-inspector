package collector

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed viewer.html
var page []byte

type Config struct {
	DB, ControlFile, ViewerHost, ProcRoot, NginxPIDFile string
	UDPPort, HTTPPort, RetentionHours, MaxEvents        int
}
type Server struct {
	Store     *Store
	Control   *Control
	udp       *net.UDPConn
	listeners []net.Listener
	servers   []*http.Server
	pending   chan Event
	mu        sync.Mutex
	stats     map[string]uint64
	failure   bool
	workers   *workerMonitor
}

func NewServer(c Config) (s *Server, err error) {
	s = &Server{workers: newWorkerMonitor(c.ProcRoot, c.NginxPIDFile), pending: make(chan Event, 2000), stats: map[string]uint64{}}
	defer func() {
		if err != nil {
			s.closeResources()
		}
	}()
	if s.Store, err = NewStore(c.DB, c.RetentionHours, c.MaxEvents); err != nil {
		return nil, err
	}
	if s.Control, err = NewControl(c.ControlFile); err != nil {
		return nil, err
	}
	if s.udp, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: c.UDPPort}); err != nil {
		return nil, err
	}
	s.udp.SetReadBuffer(4 * 1024 * 1024)
	hosts := []string{"127.0.0.1"}
	if c.ViewerHost != "" && c.ViewerHost != "127.0.0.1" {
		ip := net.ParseIP(c.ViewerHost)
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() {
			return nil, fmt.Errorf("viewer-host must be a specific local IPv4 address")
		}
		hosts = append(hosts, c.ViewerHost)
	}
	for i, host := range hosts {
		l, e := net.Listen("tcp4", net.JoinHostPort(host, strconv.Itoa(c.HTTPPort)))
		if e != nil {
			return nil, e
		}
		if i == 0 {
			c.HTTPPort = l.Addr().(*net.TCPAddr).Port
		}
		s.listeners = append(s.listeners, l)
		server := &http.Server{Handler: s.handler(i != 0, c.HTTPPort), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
		s.servers = append(s.servers, server)
	}
	return s, nil
}
func (s *Server) closeResources() {
	for _, l := range s.listeners {
		l.Close()
	}
	if s.udp != nil {
		s.udp.Close()
	}
	if s.Control != nil {
		s.Control.Close()
	}
	if s.Store != nil {
		s.Store.Close()
	}
}
func (s *Server) inc(k string, n uint64) { s.mu.Lock(); s.stats[k] += n; s.mu.Unlock() }
func (s *Server) handler(readonly bool, port int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		reply := func(v any, code int) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(v)
		}
		fail := func(msg string, code int) { reply(map[string]string{"error": msg}, code) }
		if r.Method == http.MethodPost {
			if readonly {
				fail("viewer is read-only; use loopback controls", 403)
				return
			}
			local := r.Host == fmt.Sprintf("127.0.0.1:%d", port) || r.Host == fmt.Sprintf("localhost:%d", port)
			if !local {
				fail("local host required", 403)
				return
			}
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
				fail("same origin required", 403)
				return
			}
			if r.Header.Get("Content-Type") != "application/json" {
				fail("application/json required", 415)
				return
			}
			raw, err := io.ReadAll(io.LimitReader(r.Body, 257))
			if err != nil || len(raw) == 0 || len(raw) > 256 {
				fail("invalid capture request", 400)
				return
			}
			var p map[string]json.RawMessage
			if err = json.Unmarshal(raw, &p); err != nil || p == nil || len(p) > 2 {
				fail("invalid capture request", 400)
				return
			}
			for k := range p {
				if k != "seconds" && k != "sample_permyriad" {
					fail("invalid capture request", 400)
					return
				}
			}
			switch r.URL.Path {
			case "/api/capture/start":
				var seconds *float64
				if b, ok := p["seconds"]; ok && string(b) != "null" {
					var n float64
					if err = json.Unmarshal(b, &n); err != nil {
						fail("invalid duration", 400)
						return
					}
					seconds = &n
				}
				sample := 100
				if raw, ok := p["sample_permyriad"]; ok {
					if err := json.Unmarshal(raw, &sample); err != nil {
						fail("invalid sample", 400)
						return
					}
				}
				v, err := s.Control.StartSample(seconds, sample)
				if err != nil {
					fail(err.Error(), 400)
					return
				}
				reply(v, 200)
			case "/api/capture/stop":
				if len(p) != 0 {
					fail("invalid capture request", 400)
					return
				}
				state := s.Control.Stop()
				s.workers.clear()
				reply(state, 200)
			default:
				fail("not found", 404)
			}
			return
		}
		if r.Method != http.MethodGet {
			fail("method not allowed", 405)
			return
		}
		var v any
		var err error
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(page)
			return
		case "/api/workers":
			v = s.workers.snapshot(s.Control.Status().Active)
		case "/api/capture":
			v = s.Control.Status()
		case "/health":
			s.mu.Lock()
			stats := map[string]uint64{}
			for k, n := range s.stats {
				stats[k] = n
			}
			failed := s.failure
			s.mu.Unlock()
			status := "ok"
			code := 200
			if failed {
				status = "error"
				code = 503
			}
			reply(map[string]any{"status": status, "stats": stats, "capture": s.Control.Status(), "queue": len(s.pending)}, code)
			return
		case "/api/events":
			v, err = s.Store.Recent()
		case "/api/summary":
			v, err = s.Store.Summary()
		default:
			if strings.HasPrefix(r.URL.Path, "/api/events/") {
				v, err = s.Store.Get(strings.TrimPrefix(r.URL.Path, "/api/events/"))
				if err == sql.ErrNoRows {
					fail("not found", 404)
					return
				}
			} else {
				fail("not found", 404)
				return
			}
		}
		if err != nil {
			fail("storage unavailable", 503)
			return
		}
		reply(v, 200)
	})
}
func (s *Server) receive(ctx context.Context) {
	defer close(s.pending)
	buf := make([]byte, MaxEventBytes+1)
	for {
		n, _, err := s.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if !s.Control.Status().Active {
			s.inc("inactive_dropped", 1)
			continue
		}
		var envelope struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(buf[:n], &envelope) == nil && envelope.Kind == "worker" {
			worker, err := ValidateWorker(buf[:n])
			if err != nil {
				s.inc("invalid_worker", 1)
			} else {
				if s.workers.observe(worker) {
					s.inc("worker_received", 1)
				} else {
					s.inc("worker_scope_dropped", 1)
				}
			}
			continue
		}
		v, err := Validate(buf[:n])
		if err != nil {
			s.inc("invalid", 1)
			continue
		}
		if !s.workers.accepts(v) {
			s.inc("scope_dropped", 1)
			continue
		}
		select {
		case s.pending <- v:
			s.inc("received", 1)
		case <-ctx.Done():
			return
		default:
			s.inc("queue_dropped", 1)
		}
	}
}
func (s *Server) persist() {
	for first := range s.pending {
		batch := []Event{first}
		for len(batch) < 200 {
			select {
			case e, ok := <-s.pending:
				if ok {
					batch = append(batch, e)
				} else {
					goto write
				}
			default:
				goto write
			}
		}
	write:
		if err := s.Store.Insert(batch); err != nil {
			s.mu.Lock()
			s.failure = true
			s.mu.Unlock()
			s.inc("storage_dropped", uint64(len(batch)))
		} else {
			s.inc("persisted", uint64(len(batch)))
		}
	}
}
func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer s.closeResources()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); s.workers.run(ctx) }()
	go func() { defer wg.Done(); s.receive(ctx) }()
	go func() { defer wg.Done(); s.persist() }()
	errs := make(chan error, len(s.servers))
	for i, h := range s.servers {
		go func(h *http.Server, l net.Listener) {
			if e := h.Serve(l); e != nil && e != http.ErrServerClosed {
				errs <- e
			}
		}(h, s.listeners[i])
	}
	var err error
	select {
	case <-ctx.Done():
	case err = <-errs:
	}
	s.Control.Stop()
	cancel()
	s.udp.Close()
	shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	for _, h := range s.servers {
		h.Shutdown(shutdown)
	}
	wg.Wait()
	return err
}
