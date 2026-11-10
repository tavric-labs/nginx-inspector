package collector

import (
	"fmt"
	"io"
)

// Archive volume is bounded independently from a live session's duration.
// Stream-only capture writes no archive; its storage is the bounded collector.
type traceOutput struct {
	writer     io.Writer
	remaining  int
	streamOnly bool
}

func (o *traceOutput) record(line []byte) error {
	if o.streamOnly {
		return nil
	}
	if len(line)+1 > o.remaining {
		return fmt.Errorf("64 MiB trace output limit reached")
	}
	o.remaining -= len(line) + 1
	_, err := o.writer.Write(append(append([]byte{}, line...), '\n'))
	return err
}
