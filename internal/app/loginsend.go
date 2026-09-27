package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sender"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// Where login codes go out: mail over the owner's SMTP server, and text
// messages through Tencent Cloud SMS (with the saved Tencent Cloud keys)
// or Alibaba Cloud SMS.
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

// SMSSettings is the text-message setup: Tencent Cloud SMS (with the
// saved Tencent Cloud keys) or Alibaba Cloud SMS (with its own AccessKey).
type SMSSettings struct {
	Provider string `json:"provider"` // tencent (the default) or aliyun
	// Tencent Cloud: appId, sign, template (an ID), params, region.
	// Alibaba Cloud: sign and template (SMS_...) too.
	tencent.SMS
	AccessKeyID     string `json:"accessKeyId,omitempty"`     // Alibaba Cloud
	AccessKeySecret string `json:"accessKeySecret,omitempty"` // only when saving; never shown
	CodeVar         string `json:"codeVar,omitempty"`         // Alibaba Cloud template variable for the code, e.g. code
	MinutesVar      string `json:"minutesVar,omitempty"`      // and one for the minutes, when the template has it
	DailyLimit      int    `json:"dailyLimit"`                // texts a day at most, a guard against someone running up the bill
	Configured      bool   `json:"configured"`
	Keys            bool   `json:"keys"`      // Tencent Cloud keys saved
	HasSecret       bool   `json:"hasSecret"` // Alibaba Cloud secret saved
	SentToday       int    `json:"sentToday"`
}

const aliyunSecret = "sms/aliyun_secret"

var (
	smsAppRe  = regexp.MustCompile(`^14[0-9]{8}$`)
	smsTplRe  = regexp.MustCompile(`^[0-9]{1,10}$`)
	smsSignRe = regexp.MustCompile(`^[\p{Han}A-Za-z0-9 ._-]{2,20}$`)
	aliKeyRe  = regexp.MustCompile(`^[A-Za-z0-9]{12,64}$`)
	aliTplRe  = regexp.MustCompile(`^SMS_[0-9]{3,20}$`)
	smsVarRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,31}$`)
)

func checkSMS(s SMSSettings) error {
	if !smsSignRe.MatchString(s.Sign) {
		return fmt.Errorf("签名要填审核通过的签名内容（不带【】），例如 喵面板")
	}
	if s.Provider == "aliyun" {
		switch {
		case !aliKeyRe.MatchString(s.AccessKeyID):
			return fmt.Errorf("AccessKey ID 不对，一般是 LTAI 开头的一串字母和数字")
		case !aliTplRe.MatchString(s.Template):
			return fmt.Errorf("模板 CODE 要像 SMS_123456789，在阿里云「短信服务 → 国内消息 → 模板管理」里")
		case !smsVarRe.MatchString(s.CodeVar):
			return fmt.Errorf("验证码的变量名只能用字母、数字和下划线，模板里是 ${code} 就填 code")
		case s.MinutesVar != "" && !smsVarRe.MatchString(s.MinutesVar):
			return fmt.Errorf("分钟数的变量名只能用字母、数字和下划线")
		}
		return nil
	}
	switch {
	case !smsAppRe.MatchString(s.AppID):
		return fmt.Errorf("SDK AppID 是 14 开头的 10 位数字，在腾讯云「短信 → 应用管理」里")
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
	if s.Provider == "" {
		s.Provider = "tencent"
	}
	if s.DailyLimit <= 0 {
		s.DailyLimit = smsDailyCap
	}
	s.Keys = a.tencentClient() != nil
	secret, _ := a.Secrets.Get(aliyunSecret)
	s.HasSecret = secret != ""
	ready := s.Keys
	if s.Provider == "aliyun" {
		ready = s.HasSecret
	}
	s.AccessKeySecret = ""
	s.Configured = raw != "" && checkSMS(s) == nil && ready
	return s, s.Configured
}

// GetSMSSettings returns the text-message setup.
func (a *App) GetSMSSettings() SMSSettings {
	s, _ := a.smsConfig()
	s.SentToday = a.smsCount.today()
	return s
}

func cleanSMS(s SMSSettings) SMSSettings {
	if s.Provider != "aliyun" {
		s.Provider = "tencent"
	}
	s.AppID, s.Sign, s.Template = strings.TrimSpace(s.AppID), strings.Trim(strings.TrimSpace(s.Sign), "【】[]"), strings.TrimSpace(s.Template)
	s.AccessKeyID, s.AccessKeySecret = strings.TrimSpace(s.AccessKeyID), strings.TrimSpace(s.AccessKeySecret)
	s.CodeVar, s.MinutesVar = strings.Trim(strings.TrimSpace(s.CodeVar), "${}"), strings.Trim(strings.TrimSpace(s.MinutesVar), "${}")
	if s.CodeVar == "" {
		s.CodeVar = "code"
	}
	return s
}

// SaveSMSSettings saves the text-message setup; an empty Alibaba Cloud
// secret keeps the saved one.
func (a *App) SaveSMSSettings(s SMSSettings) (SMSSettings, error) {
	s = cleanSMS(s)
	if err := checkSMS(s); err != nil {
		return SMSSettings{}, userErr("%v", err)
	}
	if s.DailyLimit <= 0 || s.DailyLimit > smsDailyLimit {
		return SMSSettings{}, userErr("每天最多发送的条数要在 1 到 %d 之间", smsDailyLimit)
	}
	if s.Provider == "aliyun" {
		if s.AccessKeySecret != "" {
			if err := a.Secrets.Set(aliyunSecret, s.AccessKeySecret); err != nil {
				return SMSSettings{}, err
			}
		} else if v, _ := a.Secrets.Get(aliyunSecret); v == "" {
			return SMSSettings{}, userErr("请填写 AccessKey Secret")
		}
	}
	stored := struct {
		Provider string `json:"provider"`
		tencent.SMS
		AccessKeyID string `json:"accessKeyId,omitempty"`
		CodeVar     string `json:"codeVar,omitempty"`
		MinutesVar  string `json:"minutesVar,omitempty"`
		DailyLimit  int    `json:"dailyLimit"`
	}{s.Provider, s.SMS, s.AccessKeyID, s.CodeVar, s.MinutesVar, s.DailyLimit}
	raw, _ := json.Marshal(stored)
	if err := a.Store.SetSetting(smsSetting, string(raw)); err != nil {
		return SMSSettings{}, err
	}
	_ = a.Store.Audit("user", "settings.sms", map[string]string{"tencent": "腾讯云短信", "aliyun": "阿里云短信"}[s.Provider], s.Sign)
	return a.GetSMSSettings(), nil
}

// ClearSMSSettings forgets the text-message setup.
func (a *App) ClearSMSSettings() error {
	_ = a.Secrets.Delete(aliyunSecret)
	_ = a.Store.Audit("user", "settings.sms", "-", "清除了短信设置")
	return a.Store.DeleteSetting(smsSetting)
}

// TestSMS sends a test code with the settings given (the saved Alibaba
// Cloud secret when none is given), before they are saved.
func (a *App) TestSMS(ctx context.Context, s SMSSettings, phone string) error {
	s = cleanSMS(s)
	if err := checkSMS(s); err != nil {
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
	var send func() error
	switch s.Provider {
	case "aliyun":
		secret := s.AccessKeySecret
		if secret == "" {
			secret, _ = a.Secrets.Get(aliyunSecret)
		}
		if secret == "" {
			return userErr("还没有填写阿里云 AccessKey Secret")
		}
		c := aliyun.New(s.AccessKeyID, secret)
		if a.AliyunEndpoint != "" {
			c.Endpoint = a.AliyunEndpoint
		}
		params := map[string]string{s.CodeVar: code}
		if s.MinutesVar != "" {
			params[s.MinutesVar] = fmt.Sprint(codeMinutes)
		}
		send = func() error { return c.SendSMS(ctx, s.Sign, s.Template, phone, params) }
	default:
		c := a.tencentClient()
		if c == nil {
			return userErr("还没有填写腾讯云密钥，腾讯云短信要用它发送")
		}
		send = func() error { return c.SendSMS(ctx, s.SMS, phone, code, codeMinutes) }
	}
	if !a.smsCount.take(s.DailyLimit) {
		return userErr("今天的短信已经发了 %d 条，达到了设置的上限", s.DailyLimit)
	}
	if err := send(); err != nil {
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
