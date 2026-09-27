package sender

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"mime"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a small SMTP server that keeps what it is sent.
type fakeSMTP struct {
	ln       net.Listener
	tls      *tls.Config
	implicit bool // TLS from the first byte (port 465 style)
	user     string
	pass     string

	mu   sync.Mutex
	got  []string // DATA of each message
	rcpt []string
	auth []string
}

func selfSigned(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, pool
}

func startSMTP(t *testing.T, mode string) *fakeSMTP {
	t.Helper()
	f := &fakeSMTP{user: "me@example.com", pass: "app-password"}
	if mode != "none" {
		cfg, pool := selfSigned(t)
		f.tls = cfg
		old := tlsConfig
		tlsConfig = func(host string) *tls.Config { return &tls.Config{ServerName: host, RootCAs: pool} }
		t.Cleanup(func() { tlsConfig = old })
	}
	var err error
	if mode == "ssl" {
		f.implicit = true
		f.ln, err = tls.Listen("tcp", "127.0.0.1:0", f.tls)
	} else {
		f.ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.ln.Close() })
	go func() {
		for {
			c, err := f.ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	say := func(s string) { w.WriteString(s + "\r\n"); w.Flush() }
	secure := f.implicit
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			ext := []string{"250-fake", "250-AUTH PLAIN"}
			if f.tls != nil && !secure {
				ext = append(ext, "250-STARTTLS")
			}
			for _, e := range ext {
				w.WriteString(e + "\r\n")
			}
			say("250 OK")
		case up == "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, f.tls)
			if tc.Handshake() != nil {
				return
			}
			c, secure = tc, true
			r, w = bufio.NewReader(tc), bufio.NewWriter(tc)
		case strings.HasPrefix(up, "AUTH PLAIN "):
			raw, _ := base64.StdEncoding.DecodeString(cmd[len("AUTH PLAIN "):])
			parts := strings.Split(string(raw), "\x00")
			f.mu.Lock()
			f.auth = append(f.auth, strings.Join(parts, "|"))
			f.mu.Unlock()
			if len(parts) == 3 && parts[1] == f.user && parts[2] == f.pass {
				say("235 ok")
			} else {
				say("535 bad credentials")
			}
		case strings.HasPrefix(up, "MAIL FROM:"):
			say("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			f.mu.Lock()
			f.rcpt = append(f.rcpt, cmd[len("RCPT TO:"):])
			f.mu.Unlock()
			say("250 ok")
		case up == "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.got = append(f.got, b.String())
			f.mu.Unlock()
			say("250 queued")
		case up == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// bodyOf decodes the base64 body and the subject of a message.
func bodyOf(t *testing.T, msg string) (subject, body string) {
	t.Helper()
	head, raw, _ := strings.Cut(msg, "\r\n\r\n")
	for _, l := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(l, "Subject: ") {
			subject, _ = new(mime.WordDecoder).DecodeHeader(l[len("Subject: "):])
		}
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(raw, "\r\n", ""))
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	return subject, string(b)
}

func TestSendMail(t *testing.T) {
	for _, mode := range []string{"ssl", "starttls", "none"} {
		t.Run(mode, func(t *testing.T) {
			f := startSMTP(t, mode)
			cfg := SMTP{Host: "127.0.0.1", Port: f.port(), Security: mode, Username: f.user, Password: f.pass, FromName: "Miao Panel"}
			err := Send(context.Background(), cfg, "owner@example.org", "登录验证码 123456", "你的验证码是 123456。\n10 分钟内有效。")
			if err != nil {
				t.Fatal(err)
			}
			if len(f.got) != 1 || f.rcpt[0] != "<owner@example.org>" {
				t.Fatalf("got %v to %v", f.got, f.rcpt)
			}
			subject, body := bodyOf(t, f.got[0])
			if subject != "登录验证码 123456" || body != "你的验证码是 123456。\r\n10 分钟内有效。" || !strings.Contains(f.got[0], "From: \"Miao Panel\" <me@example.com>") {
				t.Fatalf("message:\n%s\nsubject %q body %q", f.got[0], subject, body)
			}
		})
	}
}

func TestSendMailFailures(t *testing.T) {
	f := startSMTP(t, "ssl")
	good := SMTP{Host: "127.0.0.1", Port: f.port(), Security: "ssl", Username: f.user, Password: f.pass}
	wrong := good
	wrong.Password = "nope"
	if err := Send(context.Background(), wrong, "a@example.org", "s", "b"); err == nil || !strings.Contains(err.Error(), "授权码") {
		t.Fatalf("wrong password: %v", err)
	}
	// STARTTLS asked of a server that speaks TLS from the start.
	mismatch := good
	mismatch.Security = "starttls"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Send(ctx, mismatch, "a@example.org", "s", "b"); err == nil {
		t.Fatal("starttls to an ssl port worked")
	}
	for _, bad := range []SMTP{
		{Host: "smtp.qq.com", Port: 465, Security: "ssl", Username: "not-an-address"},
		{Host: "smtp.qq.com", Port: 0, Security: "ssl", Username: "a@qq.com"},
		{Host: "smtp.qq.com", Port: 465, Security: "tls", Username: "a@qq.com"},
		{Host: "smtp.qq.com\r\nRCPT", Port: 465, Security: "ssl", Username: "a@qq.com"},
	} {
		if bad.Check() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := Send(context.Background(), good, "a@example.org\r\nBcc: x@y.z", "s", "b"); err == nil {
		t.Fatal("header injection in the address accepted")
	}
	// Nothing listens: a clear message, not a hang.
	closed := good
	closed.Port = freePort(t)
	if err := Send(context.Background(), closed, "a@example.org", "s", "b"); err == nil || !strings.Contains(err.Error(), "连不上") {
		t.Fatalf("closed port: %v", err)
	}
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return p
}
