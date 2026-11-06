//go:build linux

package collector

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"sync/atomic"
	"unsafe"
)

type lease struct {
	file   *os.File
	memory []byte
}

func openLease(path string) (*lease, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0644)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		f.Close()
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("control must be an owned regular file with one link")
	}
	if err = f.Truncate(8); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Chmod(0644); err != nil {
		f.Close()
		return nil, err
	}
	b, err := unix.Mmap(fd, 0, 8, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, err
	}
	l := &lease{f, b}
	l.write(0)
	return l, nil
}
func (l *lease) write(v uint64) { atomic.StoreUint64((*uint64)(unsafe.Pointer(&l.memory[0])), v) }
func (l *lease) close()         { l.write(0); unix.Munmap(l.memory); l.file.Close() }
func monotonicMS() uint64 {
	var t unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &t); err != nil {
		panic(err)
	}
	return uint64(t.Sec)*1000 + uint64(t.Nsec)/1000000
}
