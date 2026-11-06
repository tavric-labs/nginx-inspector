//go:build !linux

package collector

import "fmt"

type lease struct{}

func openLease(string) (*lease, error) {
	return nil, fmt.Errorf("native capture serving requires Linux; start/stop/status/top clients work on this OS")
}
func (*lease) write(uint64) {}
func (*lease) close()       {}
func monotonicMS() uint64   { return 0 }
