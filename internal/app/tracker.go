package app

import "time"

type trackerResult struct {
	SeenGame      bool
	RestoreAt     time.Time
	ShouldRestore bool
}

type gameTracker struct {
	delay     time.Duration
	seen      bool
	missing   bool
	missingAt time.Time
	fired     bool
}

// newGameTracker starts a tracker whose countdown has not begun. seen carries "the
// watched process has been seen running during this ownership" across a watcher
// replacement; missing, missingAt and fired never carry, which is the whole reason a
// replacement gets a new object rather than the old one. A half-elapsed countdown that
// survived would restore at a moment nobody asked for, and a fired tracker would never
// arm a second time.
func newGameTracker(delay time.Duration, seen bool) *gameTracker {
	return &gameTracker{delay: delay, seen: seen}
}

func (t *gameTracker) Observe(running bool, now time.Time) trackerResult {
	if running {
		t.seen = true
		t.missing = false
		return trackerResult{SeenGame: true}
	}
	if !t.seen {
		return trackerResult{}
	}
	if !t.missing {
		t.missing = true
		t.missingAt = now
	}
	deadline := t.missingAt.Add(t.delay)
	shouldRestore := !t.fired && !now.Before(deadline)
	if shouldRestore {
		t.fired = true
	}
	return trackerResult{SeenGame: true, RestoreAt: deadline, ShouldRestore: shouldRestore}
}
