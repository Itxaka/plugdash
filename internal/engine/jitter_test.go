package engine

import (
	"testing"
	"time"
)

// TestPhaseOffset checks the first-run stagger offsets: zero for run-ASAP
// trackers, bounded by min(coldStartSpread, interval), deterministic, and
// spread across ids.
func TestPhaseOffset(t *testing.T) {
	// A 0/sub-second interval is never staggered.
	if got := phaseOffset(1, 0); got != 0 {
		t.Errorf("phaseOffset(_, 0) = %v, want 0", got)
	}
	if got := phaseOffset(123, -5); got != 0 {
		t.Errorf("phaseOffset(_, -5) = %v, want 0", got)
	}

	// Capped to the interval when the interval is shorter than coldStartSpread.
	for _, id := range []int64{1, 2, 3, 50, 9999} {
		off := phaseOffset(id, 5) // 5s < 10s spread
		if off < 0 || off >= 5*time.Second {
			t.Errorf("phaseOffset(%d, 5) = %v, want [0, 5s)", id, off)
		}
	}

	// Capped to coldStartSpread for long intervals.
	if off := phaseOffset(7, 3600); off < 0 || off >= coldStartSpread {
		t.Errorf("phaseOffset(7, 3600) = %v, want [0, %v)", off, coldStartSpread)
	}

	// Deterministic: same id+interval → same offset (so it's stable across restarts).
	first, second := phaseOffset(42, 60), phaseOffset(42, 60)
	if first != second {
		t.Errorf("phaseOffset not deterministic: %v vs %v", first, second)
	}

	// Distinct ids should generally land on distinct slots (de-sync).
	if phaseOffset(1, 60) == phaseOffset(2, 60) {
		t.Error("phaseOffset collided for ids 1 and 2")
	}
}
