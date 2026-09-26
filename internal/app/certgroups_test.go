package app

import (
	"strings"
	"testing"
	"time"
)

func TestGroupCertificates(t *testing.T) {
	at := func(s string) (string, *int) {
		tm, ok := parseAnyTime(s)
		if !ok {
			t.Fatalf("bad time %s", s)
		}
		return tm.Format(time.RFC3339), days(tm)
	}
	entry := func(e CertEntry, end string) CertEntry {
		e.NotAfter, e.DaysLeft = at(end)
		e.Level, e.Status = judge(e.DaysLeft, e.AutoRenew)
		return e
	}
	xinnetEnd := time.Now().AddDate(0, 6, 0).In(cst)
	leEnd := time.Now().AddDate(0, 2, 10).In(cst)
	entries := []CertEntry{
		entry(CertEntry{Domain: "*.miao.club", Names: []string{"*.miao.club", "miao.club"}, Source: "tencent_ssl", Where: "腾讯云 SSL 证书（ayv3FOpX）", ID: "ayv3FOpX", Issuer: "XinNet DV TLS RSA CA 2025"}, xinnetEnd.Format("2006-01-02")+" 23:59:59"),
		entry(CertEntry{Domain: "*.miao.club", Names: []string{"*.miao.club"}, Source: "tencent_ssl", Where: "腾讯云 SSL 证书（apPfi2Rx）", ID: "apPfi2Rx", Issuer: "YE2"}, leEnd.Format("2006-01-02")+" 10:00:00"),
		// 1Panel reports the same certificates in UTC.
		entry(CertEntry{Domain: "*.miao.club", Names: []string{"*.miao.club", "miao.club"}, Source: "1panel", Where: "1Panel（服务器 blog）", ID: "3", ServerID: 1,
			Issuer: "Xin Net Technology Corp.", UsedBy: []string{"blog.miao.club", "www.miao.club"}}, xinnetEnd.Format("2006-01-02")+"T15:59:59Z"),
		entry(CertEntry{Domain: "*.miao.club", Names: []string{"*.miao.club"}, Source: "1panel", Where: "1Panel（服务器 blog）", ID: "4", ServerID: 1,
			Issuer: "Let's Encrypt", AutoRenew: true, Renew: "1Panel 自动续签", CanRenew: true}, leEnd.Format("2006-01-02")+"T02:00:00Z"),
		entry(CertEntry{Domain: "sakura.vin", Names: []string{"sakura.vin", "www.sakura.vin"}, Source: "tencent_ssl", Where: "腾讯云 SSL 证书（KujqlqAp）", ID: "KujqlqAp"}, "2025-04-06 23:59:59"),
		entry(CertEntry{Domain: "*.sakura.vin", Names: []string{"*.sakura.vin"}, Source: "1panel", Where: "1Panel（服务器 blog）", ID: "5",
			AutoRenew: true, Renew: "1Panel 自动续签", UsedBy: []string{"www.sakura.vin"}}, time.Now().AddDate(0, 2, 0).Format("2006-01-02")+"T00:00:00Z"),
		// EdgeOne serves the uploaded XinNet certificate on ui.miao.club.
		{Domain: "ui.miao.club", Names: []string{"ui.miao.club"}, Source: "eo", Where: "EdgeOne 边缘（站点 miao.club）", ID: "ayv3FOpX", Renew: "跟随腾讯云 SSL 证书（见下面同名证书）", Level: "ok", Status: "还有 180 天"},
		{Domain: "shop.miao.club", Names: []string{"shop.miao.club"}, Source: "eo", Renew: "—", Level: "info", Status: "没有开启 HTTPS，访问只能用 http://"},
	}
	for i := range entries {
		if entries[i].Renew == "" {
			entries[i].Renew = "不会自动续签"
		}
	}
	live := []LiveCert{{Domain: "blog.miao.club", NotAfter: entries[0].NotAfter}}

	groups, noHTTPS := groupCertificates(entries, live)
	if len(noHTTPS) != 1 || noHTTPS[0].Domain != "shop.miao.club" {
		t.Fatalf("no https = %+v", noHTTPS)
	}
	var miao, sakura *CertGroup
	for i := range groups {
		switch groups[i].Domain {
		case "*.miao.club":
			miao = &groups[i]
		case "*.sakura.vin", "sakura.vin":
			sakura = &groups[i]
		}
	}
	if miao == nil || len(miao.Certs) != 2 {
		t.Fatalf("*.miao.club should be one group with two certificates: %+v", groups)
	}
	xinnet, le := miao.Certs[0], miao.Certs[1]
	if len(xinnet.Copies) != 2 || !xinnet.InUse || strings.Join(xinnet.UsedBy, ",") != "blog.miao.club,www.miao.club" ||
		strings.Join(xinnet.EdgeOne, ",") != "ui.miao.club" || strings.Join(xinnet.ServedOn, ",") != "blog.miao.club" ||
		xinnet.Issuer != "Xin Net Technology Corp." || xinnet.AutoRenew || xinnet.Level != "ok" {
		t.Fatalf("xinnet = %+v", xinnet)
	}
	if len(le.Copies) != 2 || le.InUse || !le.AutoRenew || le.Level != "info" || !strings.Contains(le.Status, "没发现在用") || le.Renew != "1Panel 自动续签" {
		t.Fatalf("let's encrypt = %+v", le)
	}
	if miao.Level != "ok" || !strings.Contains(strings.Join(miao.Names, ","), "miao.club") {
		t.Fatalf("group = %+v", miao)
	}
	if sakura == nil || len(sakura.Certs) != 2 || sakura.Level != "ok" {
		t.Fatalf("sakura.vin = %+v", sakura)
	}
	old := sakura.Certs[1]
	if old.InUse || old.Level != "info" || !strings.Contains(old.Status, "已过期，没发现在用") {
		t.Fatalf("an expired certificate nobody uses is not an alarm: %+v", old)
	}
	// Problems first.
	for i := 1; i < len(groups); i++ {
		if levelRank[groups[i-1].Level] > levelRank[groups[i].Level] {
			t.Fatalf("order: %s before %s", groups[i-1].Level, groups[i].Level)
		}
	}
	if !covers([]string{"*.miao.club"}, "ui.miao.club") || covers([]string{"*.miao.club"}, "a.b.miao.club") || covers([]string{"*.miao.club"}, "miao.club") {
		t.Fatal("wildcard matching")
	}
}
