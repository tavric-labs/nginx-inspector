package collector

import (
	"bytes"
	"io"
	"testing"
)

func TestArchiveBoundDoesNotEndLiveStream(t *testing.T) {
	var archive bytes.Buffer
	a := traceOutput{writer: &archive, remaining: 5}
	if e := a.record([]byte("abc")); e != nil {
		t.Fatal(e)
	}
	if e := a.record([]byte("d")); e == nil {
		t.Fatal("archive exceeded its byte bound")
	}
	if archive.String() != "abc\n" {
		t.Fatal(archive.String())
	}
	live := traceOutput{writer: io.Discard, remaining: 1, streamOnly: true}
	for i := 0; i < 10; i++ {
		if e := live.record([]byte("metadata")); e != nil {
			t.Fatal("rolling live capture ended because of archive volume", e)
		}
	}
}
