package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// A page answers at once from what is kept, gathers anew after a change
// made here, and asks live when preloading is off.
func TestPageCache(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	var n atomic.Int32
	gather := func(context.Context) (int, error) { return int(n.Add(1)), nil }

	// Nothing kept yet: it waits for the first answer.
	v, m, err := page(ctx, a, "page_test", PageLatest, gather)
	if err != nil || v != 1 || m.At.IsZero() || m.Refreshing {
		t.Fatalf("first = %d %+v %v", v, m, err)
	}
	// Kept: at once, nothing gathered.
	if v, _, _ := page(ctx, a, "page_test", PageLatest, gather); v != 1 || n.Load() != 1 {
		t.Fatalf("kept = %d, gathered %d times", v, n.Load())
	}
	// The refresh button asks live.
	if v, _, _ := page(ctx, a, "page_test", PageRefresh, gather); v != 2 {
		t.Fatalf("refresh = %d", v)
	}
	// After a change made here, the next answer is gathered after it.
	a.PagesChanged()
	if v, m, _ := page(ctx, a, "page_test", PageLatest, gather); v != 3 || m.Refreshing {
		t.Fatalf("after a change = %d %+v", v, m)
	}
	if v, _, _ := page(ctx, a, "page_test", PageLatest, gather); v != 3 {
		t.Fatalf("kept again = %d", v)
	}

	// A new start answers with the saved copy and gathers a new one.
	b := New(a.Store, a.Secrets)
	v, m, err = page(ctx, b, "page_test", PageLatest, gather)
	if err != nil || v != 3 || !m.Refreshing {
		t.Fatalf("after a restart = %d %+v %v", v, m, err)
	}
	if v, _, _ := page(ctx, b, "page_test", PageWait, gather); v != 4 {
		t.Fatalf("waited = %d", v)
	}

	// Preloading off: every answer is live, as before.
	if _, err := a.SetPreload(ctx, false); err != nil {
		t.Fatal(err)
	}
	before := n.Load()
	page(ctx, a, "page_test", PageLatest, gather)
	page(ctx, a, "page_test", PageLatest, gather)
	if n.Load() != before+2 {
		t.Errorf("gathered %d times with preloading off", n.Load()-before)
	}

	// A failed live answer says so; the kept copy stays for later.
	fail := func(context.Context) (int, error) { return 0, errors.New("down") }
	if _, _, err := page(ctx, a, "page_test", PageRefresh, fail); err == nil {
		t.Error("the failure was hidden")
	}
}

// A preload round keeps each page ready: the websites, and each site's
// detail; a checklist step that changes something makes them gather anew.
func TestPreloadPages(t *testing.T) {
	a := newApp(t)
	sv, f := panelServer(t, a)
	f.AddSite("blog.example.com", "proxy", "http://127.0.0.1:8090")
	f.AddSite("shop.example.com", "static", "")
	ctx := context.Background()

	a.preloadPages(ctx)
	if st := a.PreloadStatus(); st.Running || st.RanAt == "" || !st.Enabled {
		t.Fatalf("status = %+v", st)
	}
	keys := map[string]bool{}
	for _, k := range a.pageKeys() {
		keys[k] = true
	}
	if !keys["page_websites"] || len(keys) < 3 {
		t.Fatalf("kept %v", a.pageKeys())
	}
	// The page answers from what was kept, without asking 1Panel.
	f.AddSite("new.example.com", "static", "")
	v, m, err := a.WebsitesPage(ctx, sv.ID, PageLatest)
	if err != nil || len(v.Servers) != 1 || len(v.Servers[0].Sites) != 2 || m.At.IsZero() {
		t.Fatalf("kept list = %+v %+v %v", v, m, err)
	}
	site := v.Servers[0].Sites[0]
	if d, m, err := a.WebsitePage(ctx, sv.ID, site.ID, PageLatest); err != nil || d.Site.Domain != site.Domain || m.Refreshing {
		t.Fatalf("kept site = %+v %+v %v", d, m, err)
	}
	// A change (a checklist step finishing) shows the new state.
	e := a.startExec(store.ExecLog{ServerID: sv.ID, ServerName: sv.Name, Kind: store.ExecChange, Title: "test"})
	a.finishExec(&e, "ok", "")
	v, _, _ = a.WebsitesPage(ctx, sv.ID, PageLatest)
	if len(v.Servers[0].Sites) != 3 {
		t.Fatalf("after a change: %+v", v.Servers[0].Sites)
	}

	// Turned off, a round does nothing more than before.
	if _, err := a.SetPreload(ctx, false); err != nil {
		t.Fatal(err)
	}
	loop, cancel := context.WithCancel(ctx)
	cancel()
	a.PreloadLoop(loop) // returns at once, gathering nothing
	if st := a.PreloadStatus(); st.Enabled {
		t.Errorf("still on: %+v", st)
	}
	_ = time.Second
}
