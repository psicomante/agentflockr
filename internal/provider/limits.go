package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Size limits for what is read from an agent. They are generous for real use
// and exist so a hostile or broken agent cannot make agentflockr use unbounded
// memory (see the untrusted-data guideline).
const (
	// MaxFileSize bounds a small status or metadata file.
	MaxFileSize = 1 << 20
	// MaxOutputSize bounds what a command may print. A page of 100 sessions
	// is far below it.
	MaxOutputSize = 64 << 20
	// MaxErrorSize bounds the standard error kept from a command. Only the
	// start of it reaches a message (session.Text cuts it much shorter), so
	// the rest is read and dropped.
	MaxErrorSize = 64 << 10
	// MaxRecordSize bounds one JSON Lines record, when a transcript is read
	// for status and when its identity is checked before deletion.
	MaxRecordSize = 16 << 20
	// ReadBufferSize is the buffer for reading a transcript; a longer record
	// grows it, up to MaxRecordSize.
	ReadBufferSize = 64 << 10
)

// ReadFileLimit reads a whole file but refuses one larger than max bytes.
func ReadFileLimit(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), max)
	}
	return data, nil
}

// LimitedBuffer collects a command's output up to a size limit. Past it, the
// command's context is cancelled so it stops instead of blocking on a pipe
// nobody reads.
type LimitedBuffer struct {
	buf    bytes.Buffer
	max    int
	cancel context.CancelFunc
	over   bool
}

// NewLimitedBuffer returns a buffer holding at most max bytes; cancel is
// called when more arrive.
func NewLimitedBuffer(max int, cancel context.CancelFunc) *LimitedBuffer {
	return &LimitedBuffer{max: max, cancel: cancel}
}

func (b *LimitedBuffer) Write(p []byte) (int, error) {
	if b.over || b.buf.Len()+len(p) > b.max {
		b.over = true
		b.cancel()
		return 0, fmt.Errorf("output exceeds %d bytes", b.max)
	}
	return b.buf.Write(p)
}

// HeadBuffer keeps the first max bytes written to it and drops the rest
// without an error, so a command that prints a lot to standard error is
// never slowed or stopped, and never fills memory.
type HeadBuffer struct {
	buf bytes.Buffer
	max int
}

// NewHeadBuffer returns a buffer keeping at most max bytes.
func NewHeadBuffer(max int) *HeadBuffer { return &HeadBuffer{max: max} }

func (b *HeadBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// String returns what was kept.
func (b *HeadBuffer) String() string { return b.buf.String() }

// Bytes returns what was collected.
func (b *LimitedBuffer) Bytes() []byte { return b.buf.Bytes() }

// Exceeded reports whether the limit was hit.
func (b *LimitedBuffer) Exceeded() bool { return b.over }
