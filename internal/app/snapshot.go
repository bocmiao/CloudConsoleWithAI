package app

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// snapshots keep the latest result of slow reads (counting logs, asking
// several services), saved in the database so pages show them at once,
// even after a restart, and refresh them in the background. A refresh
// runs on its own: a page that stops waiting does not cancel it, and
// everyone asking meanwhile shares it.
type snapshots[T any] struct {
	mu      sync.Mutex
	entries map[string]*snapEntry[T]
}

type snapEntry[T any] struct {
	v       T
	loaded  bool      // v holds a result, possibly an old one
	at      time.Time // when v was gathered in this run; zero if it is old
	err     error
	running chan struct{}
}

func (s *snapshots[T]) entry(a *App, key string) *snapEntry[T] {
	if s.entries == nil {
		s.entries = map[string]*snapEntry[T]{}
	}
	e := s.entries[key]
	if e == nil {
		e = &snapEntry[T]{}
		s.entries[key] = e
	}
	if !e.loaded {
		if raw, err := a.Store.Setting(key); err == nil && raw != "" {
			var v T
			if json.Unmarshal([]byte(raw), &v) == nil {
				e.v, e.loaded = v, true
			}
		}
	}
	return e
}

// get returns a current result: one gathered within ttl unless refresh,
// otherwise a new one, waiting for it.
func (s *snapshots[T]) get(ctx context.Context, a *App, key string, ttl time.Duration, refresh bool, gather func(context.Context) (T, error)) (T, error) {
	s.mu.Lock()
	e := s.entry(a, key)
	if !refresh && e.running == nil && !e.at.IsZero() && time.Since(e.at) < ttl {
		defer s.mu.Unlock()
		return e.v, nil
	}
	done := s.refreshLocked(ctx, a, key, e, gather)
	s.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return e.v, e.err
}

// latest answers at once with what is known, refreshing it in the
// background when it is older than ttl (refreshing says so). Only with
// nothing known yet does it wait.
func (s *snapshots[T]) latest(ctx context.Context, a *App, key string, ttl time.Duration, gather func(context.Context) (T, error)) (v T, refreshing bool, err error) {
	s.mu.Lock()
	e := s.entry(a, key)
	if !e.loaded {
		s.mu.Unlock()
		v, err = s.get(ctx, a, key, ttl, false, gather)
		return v, false, err
	}
	defer s.mu.Unlock()
	if e.running != nil || e.at.IsZero() || time.Since(e.at) >= ttl {
		s.refreshLocked(ctx, a, key, e, gather)
		return e.v, true, nil
	}
	return e.v, false, nil
}

// warm starts a refresh if the result is older than ttl, without waiting.
func (s *snapshots[T]) warm(ctx context.Context, a *App, key string, ttl time.Duration, gather func(context.Context) (T, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(a, key)
	if e.running == nil && (e.at.IsZero() || time.Since(e.at) >= ttl) {
		s.refreshLocked(ctx, a, key, e, gather)
	}
}

func (s *snapshots[T]) refreshLocked(ctx context.Context, a *App, key string, e *snapEntry[T], gather func(context.Context) (T, error)) chan struct{} {
	if e.running != nil {
		return e.running
	}
	done := make(chan struct{})
	e.running = done
	origin := originOf(ctx)
	go func() {
		ctx, cancel := context.WithTimeout(withOrigin(context.Background(), origin), 10*time.Minute)
		defer cancel()
		started := time.Now()
		v, err := gather(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		e.err = err
		if err == nil {
			e.v, e.loaded, e.at = v, true, started
			if raw, err := json.Marshal(v); err == nil {
				_ = a.Store.SetSetting(key, string(raw))
			}
		}
		e.running = nil
		close(done)
	}()
	return done
}
