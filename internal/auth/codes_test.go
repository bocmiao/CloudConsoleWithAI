package auth

import (
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

type outbox struct {
	sent  []sentCode
	fail  error
	ready map[string]bool
}

type sentCode struct{ channel, to, code, purpose string }

func (o *outbox) wire(s *Service) {
	s.Send = func(_ context.Context, channel, to, code, purpose, _ string) error {
		if o.fail != nil {
			return o.fail
		}
		o.sent = append(o.sent, sentCode{channel, to, code, purpose})
		return nil
	}
	s.Ready = func(channel string) bool { return o.ready[channel] }
}

func (o *outbox) last(t *testing.T) sentCode {
	t.Helper()
	if len(o.sent) == 0 {
		t.Fatal("nothing sent")
	}
	return o.sent[len(o.sent)-1]
}

func withAccount(t *testing.T) (*Service, *clock, *outbox, string, store.User) {
	t.Helper()
	s, c := newService(t)
	o := &outbox{ready: map[string]bool{"email": true, "sms": true}}
	o.wire(s)
	setup, _ := s.SetupCode()
	tok, u, err := s.Setup("1.1.1.1", "ua", setup, "admin", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	return s, c, o, tok, u
}

func TestNormTargets(t *testing.T) {
	for raw, want := range map[string]string{"13800138000": "+8613800138000", "138 0013 8000": "+8613800138000", "+85291234567": "+85291234567",
		"008613800138000": "+8613800138000", "8613800138000": "+8613800138000"} {
		if got, err := NormPhone(raw); err != nil || got != want {
			t.Errorf("NormPhone(%q) = %q %v", raw, got, err)
		}
	}
	for _, bad := range []string{"12345", "abc", "+0123456789", ""} {
		if _, err := NormPhone(bad); err == nil {
			t.Errorf("NormPhone(%q) accepted", bad)
		}
	}
	if e, err := NormEmail(" Owner@Example.COM "); err != nil || e != "owner@example.com" {
		t.Errorf("NormEmail = %q %v", e, err)
	}
	for _, bad := range []string{"owner", "a@b", "Name <a@b.com>", "a@b.com\r\nBcc: x@y.z"} {
		if _, err := NormEmail(bad); err == nil {
			t.Errorf("NormEmail(%q) accepted", bad)
		}
	}
	if Mask("owner@example.com") != "ow***@example.com" || Mask("+8613800138000") != "+86138****8000" {
		t.Errorf("masks: %s %s", Mask("owner@example.com"), Mask("+8613800138000"))
	}
}

func TestBindAndLoginByEmail(t *testing.T) {
	s, c, o, _, u := withAccount(t)
	ctx := context.Background()

	// Binding needs the password, and proves the address with a code.
	if err := s.BeginBind(ctx, u.ID, "1.1.1.1", "email", "owner@example.com", "wrong"); code(t, err) != "bad_login" {
		t.Fatalf("bind with a wrong password: %v", err)
	}
	if err := s.BeginBind(ctx, u.ID, "1.1.1.1", "email", "Owner@Example.com", "correct horse"); err != nil {
		t.Fatal(err)
	}
	m := o.last(t)
	if m.to != "owner@example.com" || m.purpose != "bind" || len(m.code) != 6 {
		t.Fatalf("sent %+v", m)
	}
	if _, err := s.ConfirmBind(u.ID, "1.1.1.1", "email", "000000"); code(t, err) != "bad_code" && m.code != "000000" {
		t.Fatalf("wrong bind code: %v", err)
	}
	if got, err := s.ConfirmBind(u.ID, "1.1.1.1", "email", m.code); err != nil || got != "owner@example.com" {
		t.Fatalf("confirm: %q %v", got, err)
	}

	// Email login is off until turned on.
	if err := s.SendLoginCode(ctx, "2.2.2.2", "email", "owner@example.com"); code(t, err) != "method_off" {
		t.Fatalf("code while off: %v", err)
	}
	if err := s.SetMethods(u.ID, Methods{Password: true, Email: true}); err != nil {
		t.Fatal(err)
	}
	c.add(sendGap) // the binding code went to the same address
	if err := s.SendLoginCode(ctx, "2.2.2.2", "email", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	login := o.last(t)
	if login.purpose != "login" {
		t.Fatalf("sent %+v", login)
	}
	// Again at once: too soon.
	if err := s.SendLoginCode(ctx, "2.2.2.2", "email", "owner@example.com"); code(t, err) != "too_soon" {
		t.Fatalf("second code at once: %v", err)
	}
	// An address that is not bound: same answer, nothing sent.
	n := len(o.sent)
	if err := s.SendLoginCode(ctx, "2.2.2.2", "email", "stranger@example.com"); err != nil || len(o.sent) != n {
		t.Fatalf("unbound: %v, sent %d", err, len(o.sent)-n)
	}
	if _, _, err := s.LoginWithCode("2.2.2.2", "ua", "email", "stranger@example.com", "123456", ""); code(t, err) != "bad_login" {
		t.Fatalf("login for an unbound address: %v", err)
	}
	tok, got, err := s.LoginWithCode("2.2.2.2", "ua", "email", "owner@example.com", login.code, "")
	if err != nil || tok == "" || got.ID != u.ID {
		t.Fatalf("login: %v", err)
	}
	// A code works once.
	if _, _, err := s.LoginWithCode("2.2.2.2", "ua", "email", "owner@example.com", login.code, ""); code(t, err) != "bad_login" {
		t.Fatalf("code used twice: %v", err)
	}

	// Five wrong tries throw the code away.
	c.add(2 * time.Minute)
	if err := s.SendLoginCode(ctx, "3.3.3.3", "email", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	right := o.last(t).code
	for i := 0; i < codeTries; i++ {
		wrong := "000000"
		if right == wrong {
			wrong = "111111"
		}
		_, _, _ = s.LoginWithCode("3.3.3.3", "ua", "email", "owner@example.com", wrong, "")
	}
	if _, _, err := s.LoginWithCode("4.4.4.4", "ua", "email", "owner@example.com", right, ""); code(t, err) != "bad_login" {
		t.Fatalf("code after five wrong tries: %v", err)
	}

	// Codes run out.
	c.add(2 * time.Minute)
	if err := s.SendLoginCode(ctx, "5.5.5.5", "email", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	late := o.last(t).code
	c.add(codeTTL + time.Second)
	if _, _, err := s.LoginWithCode("5.5.5.5", "ua", "email", "owner@example.com", late, ""); code(t, err) != "bad_login" {
		t.Fatalf("expired code: %v", err)
	}

	// The password login takes the email as the name too.
	if _, _, err := s.Login("6.6.6.6", "ua", "OWNER@example.com", "correct horse", ""); err != nil {
		t.Fatalf("password login by email: %v", err)
	}
}

func TestCodeLoginWithTwoStep(t *testing.T) {
	s, c, o, tok, u := withAccount(t)
	ctx := context.Background()
	if err := s.BeginBind(ctx, u.ID, "1.1.1.1", "sms", "13800138000", "correct horse"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmBind(u.ID, "1.1.1.1", "sms", o.last(t).code); err != nil {
		t.Fatal(err)
	}
	ts, _ := s.BeginTOTP(u.ID)
	key, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(ts.Secret)
	if err := s.EnableTOTP(u.ID, tok, Code(key, c.t.Unix()/30)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMethods(u.ID, Methods{Password: true, SMS: true}); err != nil {
		t.Fatal(err)
	}
	c.add(time.Minute)
	if err := s.SendLoginCode(ctx, "2.2.2.2", "sms", "+86 138-0013-8000"); err != nil {
		t.Fatal(err)
	}
	sms := o.last(t)
	if sms.to != "+8613800138000" {
		t.Fatalf("sent to %q", sms.to)
	}
	// The text message replaces the password, not the second step.
	if _, _, err := s.LoginWithCode("2.2.2.2", "ua", "sms", "13800138000", sms.code, ""); code(t, err) != "need_code" {
		t.Fatalf("no app code: %v", err)
	}
	if _, _, err := s.LoginWithCode("2.2.2.2", "ua", "sms", "13800138000", sms.code, Code(key, c.t.Unix()/30+1)); err != nil {
		t.Fatalf("with the app code: %v", err)
	}
}

func TestMethodSwitches(t *testing.T) {
	s, c, o, _, u := withAccount(t)
	ctx := context.Background()
	// Nothing bound: codes cannot be turned on, the password cannot go.
	if err := s.SetMethods(u.ID, Methods{Email: true}); code(t, err) != "not_bound" {
		t.Fatalf("email without binding: %v", err)
	}
	if err := s.SetMethods(u.ID, Methods{}); code(t, err) != "no_method" {
		t.Fatalf("nothing on: %v", err)
	}
	o.ready["email"] = false
	if err := s.BeginBind(ctx, u.ID, "1.1.1.1", "email", "owner@example.com", "correct horse"); code(t, err) != "not_ready" {
		t.Fatalf("bind without mail set up: %v", err)
	}
	o.ready["email"] = true
	_ = s.BeginBind(ctx, u.ID, "1.1.1.1", "email", "owner@example.com", "correct horse")
	_, _ = s.ConfirmBind(u.ID, "1.1.1.1", "email", o.last(t).code)
	// Codes only: the password login is refused.
	if err := s.SetMethods(u.ID, Methods{Email: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login("2.2.2.2", "ua", "admin", "correct horse", ""); code(t, err) != "method_off" {
		t.Fatalf("password while off: %v", err)
	}
	// The only way left cannot be unbound.
	if err := s.Unbind(u.ID, "1.1.1.1", "email", "correct horse"); code(t, err) != "no_method" {
		t.Fatalf("unbind the last way: %v", err)
	}
	// A broken mail setup brings the password back rather than locking out.
	o.ready["email"] = false
	if m := s.ActiveMethods(); !m.Password || m.Email {
		t.Fatalf("active with mail down: %+v", m)
	}
	if _, _, err := s.Login("2.2.2.2", "ua", "admin", "correct horse", ""); err != nil {
		t.Fatalf("password with mail down: %v", err)
	}
	o.ready["email"] = true
	// The command-line reset turns the password back on.
	if err := ResetPassword(s.Store, s.Secrets, "admin", "another good one"); err != nil {
		t.Fatal(err)
	}
	if m := s.SavedMethods(); !m.Password || !m.Email {
		t.Fatalf("after reset: %+v", m)
	}
	// Someone else's address cannot be bound twice.
	other, err := s.Store.AddUser("second", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store.SetContact(other.ID, "email", "owner@example.com"); err == nil {
		t.Fatal("the same email bound to two accounts")
	}
	// A sending failure does not say whether the address is bound.
	c.add(sendGap)
	o.fail = errors.New("smtp down")
	if err := s.SendLoginCode(ctx, "9.9.9.9", "email", "owner@example.com"); code(t, err) != "send_failed" || strings.Contains(err.Error(), "smtp") {
		t.Fatalf("send failure: %v", err)
	}
}

func TestSendLimits(t *testing.T) {
	s, c, o, _, u := withAccount(t)
	ctx := context.Background()
	_ = s.BeginBind(ctx, u.ID, "1.1.1.1", "email", "owner@example.com", "correct horse")
	_, _ = s.ConfirmBind(u.ID, "1.1.1.1", "email", o.last(t).code)
	_ = s.SetMethods(u.ID, Methods{Password: true, Email: true})
	// One address asking for many different emails is stopped.
	var last error
	for i := 0; i < sendPerIP+1; i++ {
		last = s.SendLoginCode(ctx, "7.7.7.7", "email", strings.Repeat("x", i+1)+"@example.com")
	}
	if code(t, last) != "too_many" {
		t.Fatalf("many from one address: %v", last)
	}
	// One email gets at most sendPerTarget a day, from anywhere.
	for i := 0; i < sendPerTarget; i++ {
		c.add(sendGap + time.Second)
		last = s.SendLoginCode(ctx, "8.8."+string(rune('0'+i%10))+".1", "email", "owner@example.com")
	}
	c.add(sendGap + time.Second)
	if err := s.SendLoginCode(ctx, "8.9.9.9", "email", "owner@example.com"); code(t, err) != "too_many" {
		t.Fatalf("many to one email: %v (last %v)", err, last)
	}
	c.add(25 * time.Hour)
	if err := s.SendLoginCode(ctx, "8.9.9.9", "email", "owner@example.com"); err != nil {
		t.Fatalf("next day: %v", err)
	}
}
