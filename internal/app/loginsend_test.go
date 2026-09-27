package app

import (
	"context"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sender"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func TestMailSettings(t *testing.T) {
	a := newApp(t)
	if a.MailReady() {
		t.Fatal("ready before any setup")
	}
	if _, err := a.SaveMailSettings(sender.SMTP{Host: "smtp.qq.com", Port: 465, Security: "ssl", Username: "not-mail"}); err == nil {
		t.Fatal("bad address accepted")
	}
	v, err := a.SaveMailSettings(sender.SMTP{Host: " smtp.qq.com ", Port: 465, Security: "ssl", Username: "me@qq.com", Password: "auth-code", FromName: "喵面板"})
	if err != nil || !v.HasPassword || !v.Configured || v.Password != "" || v.Host != "smtp.qq.com" {
		t.Fatalf("saved = %+v %v", v, err)
	}
	// Saving again without a password keeps the one saved.
	if v, _ = a.SaveMailSettings(sender.SMTP{Host: "smtp.qq.com", Port: 587, Security: "starttls", Username: "me@qq.com"}); !v.HasPassword || v.Port != 587 {
		t.Fatalf("resaved = %+v", v)
	}
	if !a.MailReady() {
		t.Fatal("not ready")
	}
	if err := a.ClearMailSettings(); err != nil || a.MailReady() {
		t.Fatalf("clear: %v", err)
	}
	if err := a.SendCode(context.Background(), "email", "a@example.com", "123456", "login", "1.1.1.1"); err == nil {
		t.Fatal("sent without mail settings")
	}
}

func TestSMSSettingsAndCap(t *testing.T) {
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	good := SMSSettings{SMS: tencent.SMS{AppID: "1400000000", Sign: "【喵面板】", Template: "123456", Params: 2}, DailyLimit: 2}
	if _, err := a.SaveSMSSettings(good); err != nil {
		t.Fatal(err)
	}
	if a.SMSReady() {
		t.Fatal("ready without Tencent Cloud keys")
	}
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	if v := a.GetSMSSettings(); !v.Configured || v.Sign != "喵面板" {
		t.Fatalf("settings = %+v", v)
	}
	for _, bad := range []SMSSettings{
		{SMS: tencent.SMS{AppID: "123", Sign: "喵面板", Template: "1", Params: 1}, DailyLimit: 5},
		{SMS: tencent.SMS{AppID: "1400000000", Sign: "喵面板", Template: "1", Params: 3}, DailyLimit: 5},
		{SMS: tencent.SMS{AppID: "1400000000", Sign: "喵面板", Template: "1", Params: 1}, DailyLimit: 0},
		{SMS: tencent.SMS{AppID: "1400000000", Sign: "喵面板", Template: "1", Params: 1, Region: "ap-tokyo"}, DailyLimit: 5},
	} {
		if _, err := a.SaveSMSSettings(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := a.SendCode(ctx, "sms", "+8613800138000", "123456", "login", "1.1.1.1"); err != nil {
			t.Fatal(err)
		}
	}
	// The day's limit guards the bill.
	if err := a.SendCode(ctx, "sms", "+8613800138000", "123456", "login", "1.1.1.1"); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("over the limit: %v", err)
	}
	if len(f.SMS) != 2 || a.GetSMSSettings().SentToday != 2 {
		t.Fatalf("sent %d", len(f.SMS))
	}
}
