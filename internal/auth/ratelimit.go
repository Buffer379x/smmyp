package auth

import (
	"sync"
	"time"
)

// Limiter zählt Fehlversuche je Schlüssel (IP, Benutzername) in einem gleitenden Fenster.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
	now    func() time.Time
}

func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, hits: map[string][]time.Time{}, now: time.Now}
}

func (l *Limiter) prune(key string, now time.Time) []time.Time {
	h := l.hits[key]
	i := 0
	for i < len(h) && now.Sub(h[i]) >= l.window {
		i++
	}
	h = h[i:]
	if len(h) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = h
	}
	return h
}

// Blocked meldet, ob der Schlüssel gesperrt ist, und wie lange noch.
func (l *Limiter) Blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	h := l.prune(key, now)
	if len(h) < l.max {
		return false, 0
	}
	return true, l.window - now.Sub(h[0])
}

// Fail zählt einen Fehlversuch.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.hits[key] = append(l.prune(key, now), now)
	if len(l.hits) > 10_000 { // Schutz vor Speicherwachstum
		for k := range l.hits {
			l.prune(k, now)
		}
	}
}

// Reset nach erfolgreichem Login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}
