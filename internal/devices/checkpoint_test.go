package devices

import (
	"testing"
	"time"
)

func TestLastSeenIsNotCriticalWrite(t *testing.T) {
	r := New(5)
	now := time.Now()
	r.Authorize("02:00:00:00:00:01", "ap0", now)
	r.Dirty = false
	r.Fresh = false
	r.Authorize("02:00:00:00:00:01", "ap0", now.Add(time.Minute))
	if r.Dirty || !r.Fresh {
		t.Fatal("last_seen-only update must not force flash write")
	}
	if !r.State.Devices["02:00:00:00:00:01"].LastSeen.Equal(now.Add(time.Minute)) {
		t.Fatal("in-memory freshness lost")
	}
}
