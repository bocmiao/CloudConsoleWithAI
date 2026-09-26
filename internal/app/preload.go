package app

import (
	"context"
	"time"
)

// KeepWarm has the slow pages' data ready before they are opened: when
// Miao Panel starts and then every interval, it refreshes website visits
// (EdgeOne logs and every server's logs), certificates that are out of
// date and each EdgeOne site's last 24 hours. It returns when ctx ends.
func (a *App) KeepWarm(ctx context.Context, interval time.Duration) {
	ctx = withOrigin(ctx, OriginAuto)
	for {
		a.warm(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (a *App) warm(ctx context.Context) {
	if sources, err := a.VisitSources(); err == nil {
		for _, s := range sources {
			if g, err := a.gatherer(s.Key); err == nil {
				a.visits.warm(ctx, a, visitsKey(s.Key), visitsTTL, g)
			}
		}
	}
	a.certs.mu.Lock()
	if a.certs.running == nil && (a.certs.at.IsZero() || time.Since(a.certs.at) >= certCacheTTL) {
		a.refreshCertsLocked(originOf(ctx))
	}
	a.certs.mu.Unlock()
	if c := a.tencentClient(); c != nil {
		if zones, err := c.Zones(ctx); err == nil {
			for _, z := range zones {
				_, _ = a.EOAnalytics(ctx, z.ZoneName, 24, false)
			}
		}
	}
}
