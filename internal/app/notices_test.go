package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

type hookCall struct {
	path, query, contentType string
	body                     []byte
}

func hookServer(t *testing.T) (*httptest.Server, func() []hookCall) {
	var mu sync.Mutex
	var calls []hookCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, hookCall{r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type"), b})
		mu.Unlock()
		switch {
		case strings.Contains(r.URL.RawQuery, "key=bad"):
			_, _ = io.WriteString(w, `{"errcode":93000,"errmsg":"invalid webhook url"}`)
		case strings.HasPrefix(r.URL.Path, "/open-apis/"):
			_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
		default:
			_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []hookCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]hookCall(nil), calls...)
	}
}

func TestSendWebhook(t *testing.T) {
	srv, calls := hookServer(t)
	ctx := context.Background()
	cases := []struct{ path, secret, kind string }{
		{"/cgi-bin/webhook/send?key=k1", "", hookWeCom},
		{"/robot/send?access_token=t1", "SECabc", hookDingTalk},
		{"/open-apis/bot/v2/hook/h1", "fsecret", hookFeishu},
		{"/SCT12345abc.send", "", hookServerChan},
		{"/my/hook", "", hookGeneric},
	}
	for i, c := range cases {
		if k := webhookKind(srv.URL + c.path); k != c.kind {
			t.Fatalf("%s is %s", c.path, k)
		}
		if err := sendWebhook(ctx, srv.Client(), srv.URL+c.path, c.secret, "标题", "- 一行\n- **两行**"); err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		got := calls()[i]
		var m map[string]any
		_ = json.Unmarshal(got.body, &m)
		switch c.kind {
		case hookWeCom:
			md, _ := m["markdown"].(map[string]any)
			if m["msgtype"] != "markdown" || !strings.Contains(md["content"].(string), "**标题**") {
				t.Fatalf("wecom body %s", got.body)
			}
		case hookDingTalk:
			q, _ := url.ParseQuery(got.query)
			ts := q.Get("timestamp")
			if q.Get("access_token") != "t1" || ts == "" || q.Get("sign") != hmacB64("SECabc", ts+"\n"+"SECabc") || m["msgtype"] != "markdown" {
				t.Fatalf("dingtalk %s %s", got.query, got.body)
			}
		case hookFeishu:
			ts, _ := m["timestamp"].(string)
			if ts == "" || m["sign"] != hmacB64(ts+"\n"+"fsecret", "") || m["msg_type"] != "interactive" {
				t.Fatalf("feishu %s", got.body)
			}
		case hookServerChan:
			q, _ := url.ParseQuery(string(got.body))
			if got.contentType != "application/x-www-form-urlencoded" || q.Get("title") != "标题" || !strings.Contains(q.Get("desp"), "两行") {
				t.Fatalf("serverchan %s", got.body)
			}
		case hookGeneric:
			if m["title"] != "标题" || m["source"] != "Miao Panel" {
				t.Fatalf("generic %s", got.body)
			}
		}
	}
	if err := sendWebhook(ctx, srv.Client(), srv.URL+"/cgi-bin/webhook/send?key=bad", "", "t", "x"); err == nil || !strings.Contains(err.Error(), "93000") {
		t.Fatalf("robot error not reported: %v", err)
	}
	if got := clipBytes(strings.Repeat("好", 10), 7); got != "好好\n……" {
		t.Fatalf("clip = %q", got)
	}
	if m := maskURL("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abcdef123456"); strings.Contains(m, "abcdef") {
		t.Fatalf("mask = %s", m)
	}
	if fmtInt(1234567) != "1,234,567" || fmtInt(12) != "12" || pctChange(110, 100) != "↑10%" || pctChange(90, 100) != "↓10%" || pctChange(100, 100) != "持平" {
		t.Fatal("formatting")
	}
}

func TestNotices(t *testing.T) {
	a := newApp(t)
	a.CacheDir = t.TempDir()
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	fakeDNS(t)
	now := time.Now()
	hour := now.Truncate(time.Hour).Add(-time.Hour)
	yesterday := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.Local).AddDate(0, 0, -1)
	var today string
	for i := 0; i < 35; i++ {
		today += eoRecord(hour.Add(time.Duration(i)*time.Second), "45.148.10.2", "blog.example.com", "GET", []string{"/.env", "/.git/config", "/wp-login.php"}[i%3], "-", 404, "Mozilla/5.0 zgrab/0.x", "-")
	}
	today += eoRecord(hour, "7.7.7.7", "blog.example.com", "GET", "/.env", "-", 200, chromeUA, "-")
	var before string
	for i := 0; i < 5; i++ {
		before += eoRecord(yesterday.Add(time.Duration(i)*time.Minute), "61.135.211.75", "blog.example.com", "GET", "/posts/1", "-", 200, chromeUA, "-")
	}
	f.L7Logs = map[string][]tencenttest.LogPackage{"zone-abc": {
		{Domain: "blog.example.com", Name: "today.gz", Start: hour, Lines: today},
		{Domain: "blog.example.com", Name: "yesterday.gz", Start: yesterday.Truncate(time.Hour), Lines: before},
	}}
	srv, calls := hookServer(t)
	ctx := context.Background()
	if _, err := a.SaveWebhook("ftp://x", ""); err == nil {
		t.Fatal("accepted a bad address")
	}
	v, err := a.SaveWebhook(srv.URL+"/cgi-bin/webhook/send?key=secretkey123", "")
	if err != nil || v.Settings.WebhookKind != "企业微信群机器人" || strings.Contains(v.Settings.Webhook, "secretkey123") {
		t.Fatalf("webhook = %+v %v", v.Settings, err)
	}
	if err := a.TestWebhook(ctx); err != nil || len(calls()) != 1 {
		t.Fatalf("test message: %v", err)
	}
	if _, err := a.Visits(ctx, "edgeone", false); err != nil {
		t.Fatal(err)
	}
	// Not time for the report yet: alerts only.
	if _, err := a.SaveNoticeSettings(NoticeSettings{Daily: true, DailyAt: "23:59", AlertRisk: true, AlertLeak: true, AlertCert: true, AlertBlock: true}); err != nil {
		t.Fatal(err)
	}
	a.checkNotices(ctx)
	n := a.Notices()
	if len(n.Notices) != 1 || n.Unread != 1 || n.Notices[0].Kind != "alert" || n.Notices[0].Push != "ok" ||
		!strings.Contains(n.Notices[0].Text, "45.148.10.2") || !strings.Contains(n.Notices[0].Text, "blog.example.com/.env") {
		t.Fatalf("alerts = %+v", n.Notices)
	}
	a.checkNotices(ctx)
	if n := a.Notices(); len(n.Notices) != 1 {
		t.Fatalf("alerted twice: %+v", n.Notices)
	}
	// Report time: yesterday's numbers.
	if _, err := a.SaveNoticeSettings(NoticeSettings{Daily: true, DailyAt: "00:00", AlertRisk: true}); err != nil {
		t.Fatal(err)
	}
	a.checkNotices(ctx)
	a.checkNotices(ctx) // once a day
	n = a.Notices()
	if len(n.Notices) != 2 || n.Notices[0].Kind != "report" || !strings.HasPrefix(n.Notices[0].Title, "日报 · ") ||
		!strings.Contains(n.Notices[0].Text, "**EdgeOne 日志**") || !strings.Contains(n.Notices[0].Text, "PV 5") {
		t.Fatalf("report = %+v", n.Notices)
	}
	if got := calls(); len(got) != 3 {
		t.Fatalf("pushed %d times", len(got))
	}
	a.MarkNoticesRead()
	if a.UnreadNotices() != 0 {
		t.Fatal("still unread")
	}
	if v, _ := a.SaveWebhook("", ""); v.Settings.Webhook != "" {
		t.Fatal("webhook not cleared")
	}
}
