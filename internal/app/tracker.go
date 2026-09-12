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

func newGameTracker(delay time.Duration) *gameTracker {
	return &gameTracker{delay: delay}
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
