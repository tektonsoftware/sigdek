package event

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewID returns a UUIDv7 (RFC 9562): 48-bit Unix milliseconds, then random bits.
func NewID(now time.Time) string {
	var u [16]byte
	ms := uint64(now.UnixMilli())
	u[0], u[1], u[2] = byte(ms>>40), byte(ms>>32), byte(ms>>24)
	u[3], u[4], u[5] = byte(ms>>16), byte(ms>>8), byte(ms)
	_, _ = rand.Read(u[6:])
	u[6] = (u[6] & 0x0f) | 0x70 // version 7
	u[8] = (u[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(u[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
