// Package sender delivers short messages to people: email over the SMTP
// server they set up (login codes, and later reports).
package sender

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTP is how to reach a mail server.
type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"` // ssl (implicit TLS, usually 465), starttls (usually 587), none
	Username string `json:"username"`
	Password string `json:"-"`        // for QQ, 163 and Gmail: the app password (授权码), not the login password
	From     string `json:"from"`     // the address mail comes from; the username when empty
	FromName string `json:"fromName"` // shown as the sender, e.g. Miao Panel
}

// Presets are common providers' settings.
var Presets = []struct {
	ID, Name, Host string
	Port           int
	Security, Note string
}{
	{"qq", "QQ 邮箱", "smtp.qq.com", 465, "ssl", "密码填「授权码」：QQ 邮箱网页版 → 设置 → 账号 → 开启 SMTP 服务后生成"},
	{"163", "网易 163 邮箱", "smtp.163.com", 465, "ssl", "密码填「授权码」：163 邮箱网页版 → 设置 → POP3/SMTP/IMAP → 开启后生成"},
	{"exmail", "腾讯企业邮箱", "smtp.exmail.qq.com", 465, "ssl", "密码填邮箱密码，开启了安全登录的填「客户端专用密码」"},
	{"aliyun", "阿里企业邮箱", "smtp.qiye.aliyun.com", 465, "ssl", "密码填邮箱密码"},
	{"gmail", "Gmail", "smtp.gmail.com", 465, "ssl", "密码填「应用专用密码」（Google 账号开启两步验证后生成）"},
	{"outlook", "Outlook / Microsoft 365", "smtp.office365.com", 587, "starttls", "需要账号允许 SMTP 登录"},
}

// Check validates the settings before they are saved or used.
func (s SMTP) Check() error {
	switch {
	case s.Host == "" || strings.ContainsAny(s.Host, " /:@\r\n"):
		return errors.New("SMTP 服务器地址不对，例如 smtp.qq.com")
	case s.Port < 1 || s.Port > 65535:
		return errors.New("端口不对，一般是 465（SSL）或 587（STARTTLS）")
	case s.Security != "ssl" && s.Security != "starttls" && s.Security != "none":
		return errors.New("加密方式只能是 SSL、STARTTLS 或不加密")
	case strings.ContainsAny(s.Username+s.FromName, "\r\n"):
		return errors.New("用户名或发件人名称里有换行")
	}
	if _, err := mail.ParseAddress(s.from()); err != nil {
		return fmt.Errorf("发件地址 %q 不是邮箱地址", s.from())
	}
	return nil
}

func (s SMTP) from() string {
	if s.From != "" {
		return s.From
	}
	return s.Username
}

// tlsConfig is replaced by tests, to trust their own certificate.
var tlsConfig = func(host string) *tls.Config { return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12} }

// Send sends one plain-text message.
func Send(ctx context.Context, s SMTP, to, subject, body string) error {
	if err := s.Check(); err != nil {
		return err
	}
	rcpt, err := mail.ParseAddress(to)
	if err != nil || strings.ContainsAny(to, "\r\n") {
		return fmt.Errorf("收件地址 %q 不是邮箱地址", to)
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	dialer := &net.Dialer{Deadline: deadline}
	var conn net.Conn
	if s.Security == "ssl" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig(s.Host)}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("连不上邮件服务器 %s：%w", addr, err)
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("邮件服务器没有正常应答：%w", err)
	}
	defer c.Close()
	if s.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("邮件服务器不支持 STARTTLS，请改用 SSL（一般是 465 端口）")
		}
		if err := c.StartTLS(tlsConfig(s.Host)); err != nil {
			return fmt.Errorf("加密连接失败：%w", err)
		}
	}
	if s.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("邮件服务器不接受登录（没有 AUTH），请检查端口和加密方式")
		}
		// PlainAuth refuses to send the password without TLS, except to
		// this machine.
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("登录邮件服务器失败（用户名或授权码不对？）：%w", err)
		}
	}
	if err := c.Mail(s.from()); err != nil {
		return fmt.Errorf("邮件服务器拒绝了发件地址：%w", err)
	}
	if err := c.Rcpt(rcpt.Address); err != nil {
		return fmt.Errorf("邮件服务器拒绝了收件地址：%w", err)
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(message(s, rcpt.Address, subject, body)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("邮件服务器没有收下邮件：%w", err)
	}
	return c.Quit()
}

// message builds the mail: UTF-8 headers encoded, the body in base64 so
// any text survives.
func message(s SMTP, to, subject, body string) []byte {
	var b bytes.Buffer
	from := (&mail.Address{Name: s.FromName, Address: s.from()}).String()
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := s.from()[strings.LastIndex(s.from(), "@")+1:]
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.BEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", hex.EncodeToString(id), domain)
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(body, "\n", "\r\n")))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.Bytes()
}
