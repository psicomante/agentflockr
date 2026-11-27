package opencode

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// messageIDs generates msg_ IDs as OpenCode's Identifier.ascending() does
// (packages/schema/src/identifier.ts): 12 hex digits holding the low 48
// bits of milliseconds times 4096 plus a per-millisecond counter, then 14
// random base62 characters. The service accepts client IDs only with this
// prefix, and they sort among its own by time.
type messageIDs struct {
	mu      sync.Mutex
	last    int64
	counter uint64
}

func (g *messageIDs) next(now time.Time) string {
	g.mu.Lock()
	ms := now.UnixMilli()
	if ms != g.last {
		g.last, g.counter = ms, 0
	}
	g.counter++
	current := uint64(ms)*0x1000 + g.counter
	g.mu.Unlock()
	var head [6]byte
	for i := range head {
		head[i] = byte(current >> (40 - 8*i))
	}
	var random [14]byte
	rand.Read(random[:])
	tail := make([]byte, len(random))
	for i, b := range random {
		tail[i] = base62[b%62]
	}
	return "msg_" + hex.EncodeToString(head[:]) + string(tail)
}
