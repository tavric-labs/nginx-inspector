package collector

import (
	"fmt"
	"math"
	"sync"
	"time"
)

type CaptureState struct {
	Sample    int    `json:"sample_permyriad"`
	Active    bool   `json:"active"`
	Remaining *int64 `json:"remaining_seconds"`
}
type Control struct {
	mu     sync.Mutex
	lease  *lease
	active bool
	sample int
	until  time.Time
	done   chan struct{}
	exited chan struct{}
}

func NewControl(path string) (*Control, error) {
	l, e := openLease(path)
	if e != nil {
		return nil, e
	}
	c := &Control{lease: l, sample: 100, done: make(chan struct{}), exited: make(chan struct{})}
	go c.heartbeat()
	return c, nil
}
func (c *Control) publish() CaptureState {
	now := time.Now()
	if c.active && !c.until.IsZero() && !now.Before(c.until) {
		c.active = false
	}
	s := CaptureState{Active: c.active, Sample: c.sample}
	deadline := uint64(0)
	if c.active {
		ttl := 1500 * time.Millisecond
		if !c.until.IsZero() {
			left := c.until.Sub(now)
			n := int64(math.Ceil(left.Seconds()))
			s.Remaining = &n
			if left < ttl {
				ttl = left
			}
		}
		deadline = (monotonicMS() + uint64(ttl.Milliseconds())) | (uint64(c.sample) << 48)
	}
	c.lease.write(deadline)
	return s
}
func (c *Control) Status() CaptureState                         { c.mu.Lock(); defer c.mu.Unlock(); return c.publish() }
func (c *Control) Start(seconds *float64) (CaptureState, error) { return c.StartSample(seconds, 100) }
func (c *Control) StartSample(seconds *float64, sample int) (CaptureState, error) {
	if sample < 1 || sample > 10000 {
		return CaptureState{}, fmt.Errorf("sample_permyriad must be 1..10000")
	}
	if seconds != nil && (math.IsNaN(*seconds) || math.IsInf(*seconds, 0) || *seconds <= 0 || *seconds > 86400) {
		return CaptureState{}, fmt.Errorf("seconds must be positive and at most 86400")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active = true
	c.sample = sample
	c.until = time.Time{}
	if seconds != nil {
		c.until = time.Now().Add(time.Duration(*seconds * float64(time.Second)))
	}
	return c.publish(), nil
}
func (c *Control) Stop() CaptureState {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active = false
	return c.publish()
}
func (c *Control) heartbeat() {
	defer close(c.exited)
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			c.Status()
		}
	}
}
func (c *Control) Close() { c.Stop(); close(c.done); <-c.exited; c.lease.close() }
