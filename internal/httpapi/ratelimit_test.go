package httpapi

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterWindow(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLimiter(3, time.Minute)
	l.now = func() time.Time { return now }

	for i := range 3 {
		if !l.Allow("a") {
			t.Fatalf("hit %d denied", i+1)
		}
	}
	if l.Allow("a") {
		t.Fatal("4th hit allowed")
	}
	if !l.Allow("b") {
		t.Fatal("other key should have its own budget")
	}

	now = now.Add(time.Minute)
	if !l.Allow("a") {
		t.Fatal("budget should reset after the window")
	}
}

func TestLimiterBlockedDoesNotCount(t *testing.T) {
	l := newLimiter(2, time.Minute)
	for range 5 {
		if l.Blocked("a") {
			t.Fatal("Blocked must not consume budget")
		}
	}
	l.Allow("a")
	l.Allow("a")
	if !l.Blocked("a") {
		t.Fatal("key should be blocked once the limit is used")
	}
}

func TestLimiterEvictsStaleWindows(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLimiter(1, time.Minute)
	l.now = func() time.Time { return now }
	for i := range 10_001 {
		l.Allow(fmt.Sprint(i))
	}
	now = now.Add(time.Minute)
	l.Allow("fresh")
	if n := len(l.windows); n != 1 {
		t.Fatalf("windows after eviction = %d, want 1", n)
	}
}
