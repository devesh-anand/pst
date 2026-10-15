package httpapi

import (
	"sync"
	"time"
)

// limiter is a fixed-window counter per key (client IP). It is in-memory, so
// limits are per process; a multi-instance deployment would move this to Redis
// or the edge.
type limiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	now     func() time.Time
	windows map[string]*window
}

type window struct {
	start time.Time
	count int
}

func newLimiter(limit int, per time.Duration) *limiter {
	return &limiter{limit: limit, window: per, now: time.Now, windows: map[string]*window{}}
}

// Allow records a hit for key and reports whether it is within the limit.
func (l *limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.current(key)
	w.count++
	return w.count <= l.limit
}

// Blocked reports whether key has used up its limit, without recording a hit.
func (l *limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.current(key).count >= l.limit
}

func (l *limiter) current(key string) *window {
	now := l.now()
	if len(l.windows) > 10_000 {
		for k, w := range l.windows {
			if now.Sub(w.start) >= l.window {
				delete(l.windows, k)
			}
		}
	}
	w, ok := l.windows[key]
	if !ok || now.Sub(w.start) >= l.window {
		w = &window{start: now}
		l.windows[key] = w
	}
	return w
}
