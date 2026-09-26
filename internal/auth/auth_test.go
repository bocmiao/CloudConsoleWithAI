package auth

import (
	"encoding/base32"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func code(t *testing.T, err error) string {
	t.Helper()
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	return ""
}

func newService(t *testing.T) (*Service, *clock) {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	s := New(st, secrets.OpenFile(dir), dir)
	s.Cost = bcrypt.MinCost
	c := &clock{t: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	s.Now = c.now
	return s, c
}

func TestSetupAndLogin(t *testing.T) {
	s, c := newService(t)
	setup, err := s.SetupCode()
	if err != nil || len(setup) != 19 {
		t.Fatalf("setup code %q %v", setup, err)
	}
	if again, _ := s.SetupCode(); again != setup {
		t.Fatal("setup code changed between calls")
	}
	if info, err := os.Stat(filepath.Join(s.Dir, setupFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("setup file: %v %v", info, err)
	}
	if _, _, err := s.Login("1.1.1.1", "ua", "admin", "whatever12345", ""); code(t, err) != "bad_login" {
		t.Fatalf("login before setup: %v", err)
	}
	if _, _, err := s.Setup("1.1.1.1", "ua", "WRONG-CODE-0000-0000", "admin", "correct horse"); code(t, err) != "bad_setup" {
		t.Fatalf("wrong setup code: %v", err)
	}
	if _, _, err := s.Setup("1.1.1.1", "ua", setup, "admin", "short"); code(t, err) != "weak" {
		t.Fatalf("short password: %v", err)
	}
	if _, _, err := s.Setup("1.1.1.1", "ua", setup, "admin", "aaaaaaaaaaaa"); code(t, err) != "weak" {
		t.Fatalf("one letter repeated: %v", err)
	}
	// The code may be typed in lower case and without dashes.
	tok, u, err := s.Setup("1.1.1.1", "Firefox", strings.ToLower(strings.ReplaceAll(setup, "-", "")), "admin", "correct horse")
	if err != nil || tok == "" || u.Name != "admin" {
		t.Fatalf("setup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, setupFile)); !os.IsNotExist(err) {
		t.Fatal("setup code left behind")
	}
	if _, _, err := s.Setup("2.2.2.2", "ua", setup, "evil", "correct horse 2"); code(t, err) != "setup_done" {
		t.Fatalf("second setup: %v", err)
	}
	if got, ok := s.Check(tok, "1.1.1.1"); !ok || got.Name != "admin" {
		t.Fatal("session from setup does not work")
	}
	// Only the hash of the cookie is kept.
	if _, err := s.Store.GetSession(tok); err == nil {
		t.Fatal("cookie stored as is")
	}

	tok2, _, err := s.Login("3.3.3.3", "Chrome", "admin", "correct horse", "")
	if err != nil {
		t.Fatal(err)
	}
	list, _ := s.Sessions(u.ID, tok2)
	if len(list) != 2 || !list[0].Current || list[0].IP != "3.3.3.3" {
		t.Fatalf("sessions = %+v", list)
	}
	if err := s.EndSession(u.ID, list[1].Key); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Check(tok, "1.1.1.1"); ok {
		t.Fatal("ended session still works")
	}
	s.Logout(tok2)
	if _, ok := s.Check(tok2, "3.3.3.3"); ok {
		t.Fatal("logged-out session still works")
	}

	// Sessions end after 3 idle days, and after 30 days in any case.
	tok3, _, _ := s.Login("3.3.3.3", "Chrome", "admin", "correct horse", "")
	for i := 0; i < 20; i++ {
		c.add(40 * time.Hour)
		if _, ok := s.Check(tok3, "3.3.3.3"); !ok {
			if i < 17 {
				t.Fatalf("session ended after %d hours", (i+1)*40)
			}
			break
		}
		if i == 19 {
			t.Fatal("session outlived 30 days")
		}
	}
	tok4, _, _ := s.Login("3.3.3.3", "Chrome", "admin", "correct horse", "")
	c.add(IdleTimeout + time.Minute)
	if _, ok := s.Check(tok4, "3.3.3.3"); ok {
		t.Fatal("idle session still works")
	}
}

func TestLockout(t *testing.T) {
	s, c := newService(t)
	setup, _ := s.SetupCode()
	if _, _, err := s.Setup("1.1.1.1", "ua", setup, "admin", "correct horse"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < ipLimit; i++ {
		if _, _, err := s.Login("6.6.6.6", "ua", "admin", "guess"+string(rune('a'+i))+"xxxxxxx", ""); code(t, err) != "bad_login" {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	// Even the right password is refused from that address for a while...
	if _, _, err := s.Login("6.6.6.6", "ua", "admin", "correct horse", ""); code(t, err) != "locked" {
		t.Fatalf("locked address: %v", err)
	}
	// ...but the owner elsewhere can still log in.
	if _, _, err := s.Login("7.7.7.7", "ua", "admin", "correct horse", ""); err != nil {
		t.Fatalf("other address: %v", err)
	}
	c.add(lockFor + time.Second)
	if _, _, err := s.Login("6.6.6.6", "ua", "admin", "correct horse", ""); err != nil {
		t.Fatalf("after the wait: %v", err)
	}
	// Many addresses guessing one account lock the account for a while.
	for i := 0; i < userLimit; i++ {
		_, _, _ = s.Login("10.0.0."+string(rune('0'+i%10))+string(rune('0'+i/10)), "ua", "admin", "wrong password", "")
	}
	if _, _, err := s.Login("8.8.8.8", "ua", "admin", "correct horse", ""); code(t, err) != "locked" {
		t.Fatalf("account lock: %v", err)
	}
	// Where the owner logged in before, the account's lock does not apply.
	if _, _, err := s.Login("7.7.7.7", "ua", "admin", "correct horse", ""); err != nil {
		t.Fatalf("owner's address during the account lock: %v", err)
	}
	// The command line gets the owner back in.
	if err := ResetPassword(s.Store, s.Secrets, "admin", "brand new password"); err != nil {
		t.Fatal(err)
	}
	c.add(lockFor + time.Second)
	if _, _, err := s.Login("8.8.8.8", "ua", "admin", "brand new password", ""); err != nil {
		t.Fatalf("after reset: %v", err)
	}
}

func TestTwoStep(t *testing.T) {
	s, c := newService(t)
	setup, _ := s.SetupCode()
	tok, u, err := s.Setup("1.1.1.1", "ua", setup, "admin", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableTOTP(u.ID, tok, "123456"); code(t, err) != "not_found" {
		t.Fatalf("enable before begin: %v", err)
	}
	ts, err := s.BeginTOTP(u.ID)
	if err != nil || !strings.HasPrefix(ts.QR, "data:image/png;base64,") || !strings.Contains(ts.URI, "otpauth://totp/Miao%20Panel:admin?secret="+ts.Secret) {
		t.Fatalf("begin = %+v %v", ts, err)
	}
	key, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(ts.Secret)
	step := func() int64 { return c.t.Unix() / 30 }
	if err := s.EnableTOTP(u.ID, tok, "000000"); code(t, err) != "bad_code" && Code(key, step()) != "000000" {
		t.Fatalf("wrong code: %v", err)
	}
	other, _, _ := s.Login("2.2.2.2", "ua", "admin", "correct horse", "")
	if err := s.EnableTOTP(u.ID, tok, Code(key, step())); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Check(other, "2.2.2.2"); ok {
		t.Fatal("other session survived turning two-step login on")
	}
	// A session alone cannot swap the key.
	if _, err := s.BeginTOTP(u.ID); code(t, err) != "totp_on" {
		t.Fatalf("new key while on: %v", err)
	}
	// Now the password alone is not enough.
	if _, _, err := s.Login("1.1.1.1", "ua", "admin", "correct horse", ""); code(t, err) != "need_code" {
		t.Fatalf("no code: %v", err)
	}
	c.add(30 * time.Second)
	now := Code(key, step())
	if _, _, err := s.Login("1.1.1.1", "ua", "admin", "correct horse", now); err != nil {
		t.Fatalf("with code: %v", err)
	}
	// A code works once, also after a restart.
	if _, _, err := s.Login("1.1.1.1", "ua", "admin", "correct horse", now); code(t, err) != "bad_code" {
		t.Fatalf("replayed code: %v", err)
	}
	restarted := New(s.Store, s.Secrets, s.Dir)
	restarted.Cost, restarted.Now = s.Cost, c.now
	if _, _, err := restarted.Login("1.1.1.1", "ua", "admin", "correct horse", now); code(t, err) != "bad_code" {
		t.Fatalf("replayed code after a restart: %v", err)
	}
	// A phone a little slow is fine; a code from long ago is not.
	c.add(30 * time.Second)
	if _, _, err := s.Login("1.1.1.1", "ua", "admin", "correct horse", Code(key, step()-5)); code(t, err) != "bad_code" {
		t.Fatalf("old code: %v", err)
	}
	if _, _, err := s.Login("1.1.1.1", "ua", "admin", "correct horse", Code(key, step()+1)); err != nil {
		t.Fatalf("code of the next step: %v", err)
	}
	if err := s.DisableTOTP(u.ID, tok, "1.1.1.1", "wrong"); code(t, err) != "bad_login" {
		t.Fatalf("disable with wrong password: %v", err)
	}
	if err := s.DisableTOTP(u.ID, tok, "1.1.1.1", "correct horse"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login("1.1.1.1", "ua", "admin", "correct horse", ""); err != nil {
		t.Fatalf("after disabling: %v", err)
	}

	// Changing the password logs out every other session.
	other, _, _ = s.Login("2.2.2.2", "ua", "admin", "correct horse", "")
	if err := s.ChangePassword(u.ID, tok, "1.1.1.1", "wrong", "another good one"); code(t, err) != "bad_login" {
		t.Fatalf("wrong old password: %v", err)
	}
	if err := s.ChangePassword(u.ID, tok, "1.1.1.1", "correct horse", "another good one"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Check(tok, "1.1.1.1"); !ok {
		t.Fatal("own session ended")
	}
	if _, ok := s.Check(other, "2.2.2.2"); ok {
		t.Fatal("other session survived a password change")
	}
}

// Code matches RFC 6238's test vector (SHA-1, 8 digits → last 6).
func TestCodeVector(t *testing.T) {
	key := []byte("12345678901234567890")
	if got := Code(key, 59/30); got != "287082" {
		t.Fatalf("T=59: %s", got)
	}
	if got := Code(key, 1111111109/30); got != "081804" {
		t.Fatalf("T=1111111109: %s", got)
	}
}

// An unknown name costs as much as a wrong password.
func TestNoNameProbing(t *testing.T) {
	s, _ := newService(t)
	s.Cost = 11
	setup, _ := s.SetupCode()
	if _, _, err := s.Setup("1.1.1.1", "ua", setup, "admin", "correct horse"); err != nil {
		t.Fatal(err)
	}
	cost, err := bcrypt.Cost(s.dummyHash())
	if err != nil || cost != s.Cost {
		t.Fatalf("dummy cost %d, want %d (%v)", cost, s.Cost, err)
	}
	u, _ := s.Store.UserByName("admin")
	if c, _ := bcrypt.Cost([]byte(u.Password)); c != cost {
		t.Fatalf("real %d vs dummy %d", c, cost)
	}
}

// Wrong passwords sent all at once still get only the address's tries.
func TestLockoutAtOnce(t *testing.T) {
	s, _ := newService(t)
	setup, _ := s.SetupCode()
	if _, _, err := s.Setup("1.1.1.1", "ua", setup, "admin", "correct horse"); err != nil {
		t.Fatal(err)
	}
	const n = 60
	results := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() {
			_, _, err := s.Login("6.6.6.6", "ua", "admin", "wrong password", "")
			var e *Error
			errors.As(err, &e)
			results <- e.Code
		}()
	}
	checked := 0
	for i := 0; i < n; i++ {
		switch <-results {
		case "bad_login":
			checked++
		case "locked":
		default:
			t.Fatal("unexpected answer")
		}
	}
	if checked > ipLimit {
		t.Fatalf("%d passwords checked at once, limit %d", checked, ipLimit)
	}
}

// The address the owner logged in from is remembered across restarts.
func TestKnownAddressAfterRestart(t *testing.T) {
	s, c := newService(t)
	setup, _ := s.SetupCode()
	if _, _, err := s.Setup("1.1.1.1", "ua", setup, "admin", "correct horse"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login("2.2.2.2", "ua", "admin", "correct horse", ""); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.Store, s.Secrets, s.Dir)
	restarted.Cost, restarted.Now = s.Cost, c.now
	for i := 0; i < userLimit+2; i++ { // from many addresses, so only the account locks
		_, _, _ = restarted.Login("10.0.0."+string(rune('a'+i)), "ua", "admin", "wrong password", "")
	}
	if _, _, err := restarted.Login("3.3.3.3", "ua", "admin", "correct horse", ""); code(t, err) != "locked" {
		t.Fatalf("a new address should wait: %v", err)
	}
	if _, _, err := restarted.Login("2.2.2.2", "ua", "admin", "correct horse", ""); err != nil {
		t.Fatalf("the owner's address is still spared after a restart: %v", err)
	}
}
