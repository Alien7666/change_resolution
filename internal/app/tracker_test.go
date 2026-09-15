package app

import (
	"testing"
	"time"
)

func TestTrackerRestoresOnlyAfterSeenGameHasBeenMissingForDelay(t *testing.T) {
	now := time.Unix(100, 0)
	tracker := newGameTracker(3*time.Second, false)
	if got := tracker.Observe(false, now); got.SeenGame || got.ShouldRestore {
		t.Fatalf("unseen observation = %+v", got)
	}
	tracker.Observe(true, now)
	firstAbsent := now.Add(2 * time.Second)
	deadline := firstAbsent.Add(3 * time.Second)
	for _, at := range []time.Time{firstAbsent, now.Add(3 * time.Second), deadline.Add(-time.Nanosecond)} {
		got := tracker.Observe(false, at)
		if !got.SeenGame || got.ShouldRestore || !got.RestoreAt.Equal(deadline) {
			t.Fatalf("at %v: got %+v, want pending until %v", at, got, deadline)
		}
	}
	if got := tracker.Observe(false, deadline); !got.ShouldRestore {
		t.Fatalf("at deadline: %+v", got)
	}
	if got := tracker.Observe(false, deadline.Add(time.Second)); got.ShouldRestore {
		t.Fatal("restore must fire only once per tracker")
	}
}

func TestTrackerCancelsPendingRestoreWhenGameReturns(t *testing.T) {
	now := time.Unix(100, 0)
	tracker := newGameTracker(3*time.Second, false)
	tracker.Observe(true, now)
	tracker.Observe(false, now.Add(time.Second))
	if got := tracker.Observe(true, now.Add(3*time.Second)); !got.RestoreAt.IsZero() || got.ShouldRestore {
		t.Fatalf("return did not cancel restore: %+v", got)
	}
	got := tracker.Observe(false, now.Add(4*time.Second))
	if got.ShouldRestore || !got.RestoreAt.Equal(now.Add(7*time.Second)) {
		t.Fatalf("new absence did not restart delay: %+v", got)
	}
	if !tracker.Observe(false, now.Add(7*time.Second)).ShouldRestore {
		t.Fatal("new deadline did not restore")
	}
}

func TestTrackerNeverRestoresWhenGameWasNeverSeen(t *testing.T) {
	tracker := newGameTracker(3*time.Second, false)
	for _, seconds := range []int64{0, 100, 1000000} {
		got := tracker.Observe(false, time.Unix(seconds, 0))
		if got.SeenGame || got.ShouldRestore || !got.RestoreAt.IsZero() {
			t.Fatalf("unseen observation = %+v", got)
		}
	}
}

func TestTrackerHandlesZeroTimeAsFirstAbsence(t *testing.T) {
	tracker := newGameTracker(3*time.Second, false)
	tracker.Observe(true, time.Time{})
	tracker.Observe(false, time.Time{})
	if !tracker.Observe(false, time.Time{}.Add(3*time.Second)).ShouldRestore {
		t.Fatal("zero timestamp must be a valid first absence")
	}
}
