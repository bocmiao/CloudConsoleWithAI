package app

import (
	"context"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
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

func TestAliyunSMS(t *testing.T) {
	a := newApp(t)
	f := aliyuntest.Start(t)
	f.Vars = []string{"code", "min"}
	a.AliyunEndpoint = f.URL
	s := SMSSettings{Provider: "aliyun", SMS: tencent.SMS{Sign: "喵面板", Template: "SMS_123456789"}, AccessKeyID: aliyuntest.ID,
		CodeVar: "${code}", MinutesVar: "min", DailyLimit: 5}
	// The secret is needed the first time.
	if _, err := a.SaveSMSSettings(s); err == nil || !strings.Contains(err.Error(), "Secret") {
		t.Fatalf("no secret: %v", err)
	}
	// Tested before saving, with the secret typed in.
	s.AccessKeySecret = aliyuntest.Secret
	if err := a.TestSMS(context.Background(), s, "+8613800138000"); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 1 || f.Sent[0].Phone != "13800138000" || f.Sent[0].Params["code"] != "123456" || f.Sent[0].Params["min"] != "10" {
		t.Fatalf("test sent %+v", f.Sent)
	}
	v, err := a.SaveSMSSettings(s)
	if err != nil || !v.Configured || !v.HasSecret || v.AccessKeySecret != "" || v.CodeVar != "code" || v.Keys {
		t.Fatalf("saved = %+v %v", v, err)
	}
	// Saved again without the secret: the saved one stays.
	s.AccessKeySecret = ""
	if _, err := a.SaveSMSSettings(s); err != nil || !a.SMSReady() {
		t.Fatalf("resave: %v", err)
	}
	if err := a.SendCode(context.Background(), "sms", "+85291234567", "654321", "login", "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	if f.Sent[1].Phone != "85291234567" || f.Sent[1].Params["code"] != "654321" {
		t.Fatalf("sent %+v", f.Sent[1])
	}
	for _, bad := range []SMSSettings{
		{Provider: "aliyun", SMS: tencent.SMS{Sign: "喵面板", Template: "123456"}, AccessKeyID: aliyuntest.ID, DailyLimit: 5},
		{Provider: "aliyun", SMS: tencent.SMS{Sign: "喵面板", Template: "SMS_123456789"}, AccessKeyID: "bad id!", DailyLimit: 5},
		{Provider: "aliyun", SMS: tencent.SMS{Sign: "喵面板", Template: "SMS_123456789"}, AccessKeyID: aliyuntest.ID, CodeVar: "a-b", DailyLimit: 5},
	} {
		if _, err := a.SaveSMSSettings(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := a.ClearSMSSettings(); err != nil || a.SMSReady() || a.GetSMSSettings().HasSecret {
		t.Fatalf("clear: %v", err)
	}
}
