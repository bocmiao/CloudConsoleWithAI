package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sender"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// Where login codes go out: mail over the owner's SMTP server, and text
// messages through Tencent Cloud SMS with the saved Tencent Cloud keys.
const (
	mailSetting   = "mail.smtp"
	mailSecret    = "mail/smtp_password"
	smsSetting    = "sms.tencent"
	codeMinutes   = 10
	smsDailyCap   = 30 // default: texts a day, whoever asks
	smsDailyLimit = 500
)

// MailSettings is the SMTP setup as the page shows it: never the password.
type MailSettings struct {
	sender.SMTP
	HasPassword bool `json:"hasPassword"`
	Configured  bool `json:"configured"`
}

// MailPreset is a provider's usual settings.
type MailPreset struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Note     string `json:"note"`
}

// MailPresets lists common providers.
func MailPresets() []MailPreset {
	var out []MailPreset
	for _, p := range sender.Presets {
		out = append(out, MailPreset{p.ID, p.Name, p.Host, p.Port, p.Security, p.Note})
	}
	return out
}

func (a *App) mailConfig() (sender.SMTP, bool) {
	var s sender.SMTP
	raw, _ := a.Store.Setting(mailSetting)
	if raw == "" || json.Unmarshal([]byte(raw), &s) != nil {
		return s, false
	}
	s.Password, _ = a.Secrets.Get(mailSecret)
	return s, s.Check() == nil
}

// GetMailSettings returns the SMTP setup.
func (a *App) GetMailSettings() MailSettings {
	s, ok := a.mailConfig()
	v := MailSettings{SMTP: s, HasPassword: s.Password != "", Configured: ok}
	v.Password = ""
	return v
}

// SaveMailSettings saves the SMTP setup; an empty password keeps the
// saved one.
func (a *App) SaveMailSettings(s sender.SMTP) (MailSettings, error) {
	s.Host = strings.TrimSpace(s.Host)
	s.Username, s.From, s.FromName = strings.TrimSpace(s.Username), strings.TrimSpace(s.From), strings.TrimSpace(s.FromName)
	if err := s.Check(); err != nil {
		return MailSettings{}, userErr("%v", err)
	}
	if s.Password != "" {
		if err := a.Secrets.Set(mailSecret, s.Password); err != nil {
			return MailSettings{}, err
		}
	}
	s.Password = ""
	raw, _ := json.Marshal(s)
	if err := a.Store.SetSetting(mailSetting, string(raw)); err != nil {
		return MailSettings{}, err
	}
	_ = a.Store.Audit("user", "settings.mail", s.Host, s.Username)
	return a.GetMailSettings(), nil
}

// ClearMailSettings forgets the SMTP setup.
func (a *App) ClearMailSettings() error {
	_ = a.Secrets.Delete(mailSecret)
	_ = a.Store.Audit("user", "settings.mail", "-", "清除了发信设置")
	return a.Store.DeleteSetting(mailSetting)
}

// TestMail sends a test message with the settings given (the saved
// password when none is given), before they are saved.
func (a *App) TestMail(ctx context.Context, s sender.SMTP, to string) error {
	if s.Password == "" {
		s.Password, _ = a.Secrets.Get(mailSecret)
	}
	err := sender.Send(ctx, s, strings.TrimSpace(to), "Miao Panel 测试邮件",
		"这是 Miao Panel（喵面板）发出的测试邮件。收到它说明发信设置是对的，可以用邮箱验证码登录了。")
	if err != nil {
		return userErr("%v", err)
	}
	return nil
}

// SMSSettings is the text-message setup.
type SMSSettings struct {
	tencent.SMS
	DailyLimit int  `json:"dailyLimit"` // texts a day at most, a guard against someone running up the bill
	Configured bool `json:"configured"`
	Keys       bool `json:"keys"` // Tencent Cloud keys are saved
	SentToday  int  `json:"sentToday"`
}

var (
	smsAppRe  = regexp.MustCompile(`^14[0-9]{8}$`)
	smsTplRe  = regexp.MustCompile(`^[0-9]{1,10}$`)
	smsSignRe = regexp.MustCompile(`^[\p{Han}A-Za-z0-9 ._-]{2,20}$`)
)

func checkSMS(s tencent.SMS) error {
	switch {
	case !smsAppRe.MatchString(s.AppID):
		return fmt.Errorf("SDK AppID 是 14 开头的 10 位数字，在腾讯云「短信 → 应用管理」里")
	case !smsSignRe.MatchString(s.Sign):
		return fmt.Errorf("签名要填审核通过的签名内容（不带【】），例如 喵面板")
	case !smsTplRe.MatchString(s.Template):
		return fmt.Errorf("模板 ID 是数字，在腾讯云「短信 → 正文模板管理」里")
	case s.Params != 1 && s.Params != 2:
		return fmt.Errorf("模板里的变量只能是 1 个（验证码）或 2 个（验证码、有效分钟数）")
	case s.Region != "" && s.Region != "ap-guangzhou" && s.Region != "ap-beijing" && s.Region != "ap-nanjing":
		return fmt.Errorf("地域只能是广州、北京或南京")
	}
	return nil
}

func (a *App) smsConfig() (SMSSettings, bool) {
	var s SMSSettings
	raw, _ := a.Store.Setting(smsSetting)
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	if s.DailyLimit <= 0 {
		s.DailyLimit = smsDailyCap
	}
	s.Keys = a.tencentClient() != nil
	s.Configured = raw != "" && checkSMS(s.SMS) == nil && s.Keys
	return s, s.Configured
}

// GetSMSSettings returns the text-message setup.
func (a *App) GetSMSSettings() SMSSettings {
	s, _ := a.smsConfig()
	s.SentToday = a.smsCount.today()
	return s
}

// SaveSMSSettings saves the text-message setup.
func (a *App) SaveSMSSettings(s SMSSettings) (SMSSettings, error) {
	s.AppID, s.Sign, s.Template = strings.TrimSpace(s.AppID), strings.Trim(strings.TrimSpace(s.Sign), "【】[]"), strings.TrimSpace(s.Template)
	if err := checkSMS(s.SMS); err != nil {
		return SMSSettings{}, userErr("%v", err)
	}
	if s.DailyLimit <= 0 || s.DailyLimit > smsDailyLimit {
		return SMSSettings{}, userErr("每天最多发送的条数要在 1 到 %d 之间", smsDailyLimit)
	}
	raw, _ := json.Marshal(struct {
		tencent.SMS
		DailyLimit int `json:"dailyLimit"`
	}{s.SMS, s.DailyLimit})
	if err := a.Store.SetSetting(smsSetting, string(raw)); err != nil {
		return SMSSettings{}, err
	}
	_ = a.Store.Audit("user", "settings.sms", s.AppID, s.Sign)
	return a.GetSMSSettings(), nil
}

// ClearSMSSettings forgets the text-message setup.
func (a *App) ClearSMSSettings() error {
	_ = a.Store.Audit("user", "settings.sms", "-", "清除了短信设置")
	return a.Store.DeleteSetting(smsSetting)
}

// TestSMS sends a test code with the settings given.
func (a *App) TestSMS(ctx context.Context, s SMSSettings, phone string) error {
	if err := checkSMS(s.SMS); err != nil {
		return userErr("%v", err)
	}
	s.DailyLimit = a.GetSMSSettings().DailyLimit
	return a.sendSMS(ctx, s, phone, "123456")
}

// smsCounter counts texts sent today, to cap them.
type smsCounter struct {
	mu  sync.Mutex
	day string
	n   int
}

func (c *smsCounter) today() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.day != time.Now().Format("2006-01-02") {
		return 0
	}
	return c.n
}

// take counts one more text unless the day's limit is reached.
func (c *smsCounter) take(limit int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := time.Now().Format("2006-01-02"); c.day != d {
		c.day, c.n = d, 0
	}
	if c.n >= limit {
		return false
	}
	c.n++
	return true
}

func (a *App) sendSMS(ctx context.Context, s SMSSettings, phone, code string) error {
	c := a.tencentClient()
	if c == nil {
		return userErr("还没有填写腾讯云密钥，短信通过腾讯云发送")
	}
	if !a.smsCount.take(s.DailyLimit) {
		return userErr("今天的短信已经发了 %d 条，达到了设置的上限", s.DailyLimit)
	}
	if err := c.SendSMS(ctx, s.SMS, phone, code, codeMinutes); err != nil {
		return userErr("%v", err)
	}
	return nil
}

// MailReady and SMSReady say whether codes can go out that way.
func (a *App) MailReady() bool { _, ok := a.mailConfig(); return ok }

// SMSReady says whether text messages can be sent.
func (a *App) SMSReady() bool { _, ok := a.smsConfig(); return ok }

// SendCode delivers a login or binding code; the auth package calls it.
// ip is who asked, told to the owner in the mail.
func (a *App) SendCode(ctx context.Context, channel, to, code, purpose, ip string) error {
	what := map[string]string{"login": "登录", "bind": "绑定"}[purpose]
	switch channel {
	case "email":
		s, ok := a.mailConfig()
		if !ok {
			return userErr("还没有设置发信邮箱")
		}
		body := fmt.Sprintf("你的 Miao Panel %s验证码是：%s\n\n%d 分钟内有效，只能用一次。\n请求来自 IP %s，时间 %s。\n\n如果不是你本人在操作，请忽略这封邮件，并到 Miao Panel「设置 → 账号与安全」检查登录的设备、修改密码。",
			what, code, codeMinutes, ip, time.Now().Format("2006-01-02 15:04:05"))
		if err := sender.Send(ctx, s, to, fmt.Sprintf("Miao Panel %s验证码：%s", what, code), body); err != nil {
			return userErr("%v", err)
		}
		return nil
	case "sms":
		s, ok := a.smsConfig()
		if !ok {
			return userErr("还没有设置短信")
		}
		return a.sendSMS(ctx, s, to, code)
	}
	return userErr("不支持的发送方式 %s", channel)
}
