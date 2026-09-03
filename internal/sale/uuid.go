package sale

import (
	"crypto/rand"
	"fmt"
)

// newUUID makes a random (v4) UUID. The reservation id must be known before
// the Postgres INSERT so it can also key the Redis claim marker (spec 7.9),
// so it's generated here instead of left to Postgres's gen_random_uuid().
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
