// Package auth logs people in to the web edition of Miao Panel.
//
// There is one kind of account: a name and a password (stored as a bcrypt
// hash), plus, when turned on, a code from an authenticator app (TOTP,
// RFC 6238). A login is a random cookie; the database keeps only its
// SHA-256, so a copy of the database cannot be used to log in. Guessing is
// slowed down per address and per account. The first account can only be
// made with a setup code written to the server's log and data directory,
// so whoever finds the page first cannot take the panel over.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"rsc.io/qr"

	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

const (
	// IdleTimeout ends a session nobody used for this long; MaxAge ends
	// any session this old.
	IdleTimeout = 72 * time.Hour
	MaxAge      = 30 * 24 * time.Hour

	// Guessing: an address gets ipLimit wrong tries and an account
	// userLimit (from anywhere) within window, then waits lockFor.
	window    = 15 * time.Minute
	ipLimit   = 5
	userLimit = 20
	lockFor   = 15 * time.Minute

	// MinPassword is the shortest password accepted, in characters.
	MinPassword = 10

	setupFile = "setup-code"
	issuer    = "Miao Panel"
)

// Error is a refusal to show as it is. Code tells the page what to do:
// bad_login, need_code, bad_code, locked, bad_setup, setup_done, weak,
// bad_name, not_found.
type Error struct {
	Msg  string
	Code string
}

func (e *Error) Error() string { return e.Msg }

func refuse(code, format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...), Code: code}
}

// Service checks logins and sessions.
type Service struct {
	Store   *store.Store
	Secrets secrets.Store
	Dir     string           // where the setup code is kept
	Now     func() time.Time // tests move the clock
	Cost    int              // bcrypt cost; tests lower it

	mu       sync.Mutex
	byIP     map[string]*tries
	byUser   map[string]*tries
	goodIP   map[string]time.Time // addresses that logged in lately
	lastStep map[int64]int64      // TOTP steps already used, so a code works once

	dummyOnce sync.Once
	dummy     []byte
}

type tries struct {
	n            int
	first, until time.Time
}

// New makes the service; dir is the data directory.
func New(st *store.Store, sec secrets.Store, dir string) *Service {
	return &Service{Store: st, Secrets: sec, Dir: dir, Now: time.Now, Cost: 12}
}

var nameRe = regexp.MustCompile(`^[\p{L}\p{N}._@-]{2,32}$`)

func checkName(name string) error {
	if !nameRe.MatchString(name) {
		return refuse("bad_name", "用户名要 2 到 32 个字，只能用字母、数字、中文和 . _ @ -")
	}
	return nil
}

func checkPassword(name, pw string) error {
	switch {
	case utf8.RuneCountInString(pw) < MinPassword:
		return refuse("weak", "密码至少要 %d 个字符", MinPassword)
	case len(pw) > 72:
		return refuse("weak", "密码太长了（最多 72 个字节）")
	case strings.EqualFold(pw, name) || strings.Trim(pw, string([]rune(pw)[:1])) == "":
		return refuse("weak", "这个密码太容易猜了")
	}
	return nil
}

func (s *Service) hash(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), s.Cost)
	return string(h), err
}

// dummyHash is compared against when the name is unknown, so a wrong name
// takes as long as a wrong password (same cost) and names cannot be probed.
func (s *Service) dummyHash() []byte {
	s.dummyOnce.Do(func() {
		s.dummy, _ = bcrypt.GenerateFromPassword([]byte("miao-panel-no-such-user"), s.Cost)
	})
	return s.dummy
}

// ---- First account ----

// NeedsSetup says whether no account exists yet.
func (s *Service) NeedsSetup() (bool, error) {
	n, err := s.Store.CountUsers()
	return n == 0, err
}

// SetupCode returns the code that allows making the first account,
// writing a new one to the data directory when there is none. It is empty
// once an account exists.
func (s *Service) SetupCode() (string, error) {
	need, err := s.NeedsSetup()
	if err != nil || !need {
		return "", err
	}
	path := filepath.Join(s.Dir, setupFile)
	if b, err := os.ReadFile(path); err == nil {
		if c := strings.TrimSpace(string(b)); len(c) >= 16 {
			return c, nil
		}
	}
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	raw := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b) // 16 characters, 80 bits
	code := raw[0:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:16]
	if err := os.WriteFile(path, []byte(code+"\n"), 0o600); err != nil {
		return "", err
	}
	return code, nil
}

func normCode(c string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(c)))
}

// Setup makes the first account and logs it in.
func (s *Service) Setup(ip, ua, code, name, password string) (string, store.User, error) {
	if err := s.locked(ip, ""); err != nil {
		return "", store.User{}, err
	}
	need, err := s.NeedsSetup()
	if err != nil {
		return "", store.User{}, err
	}
	if !need {
		return "", store.User{}, refuse("setup_done", "管理员账号已经设置过了，请直接登录")
	}
	want, _ := s.SetupCode()
	if want == "" || subtle.ConstantTimeCompare([]byte(normCode(code)), []byte(normCode(want))) != 1 {
		s.fail(ip, "")
		return "", store.User{}, refuse("bad_setup", "初始化码不对。它在服务器上 Miao Panel 的日志里，也在数据目录的 setup-code 文件里")
	}
	name = strings.TrimSpace(name)
	if err := checkName(name); err != nil {
		return "", store.User{}, err
	}
	if err := checkPassword(name, password); err != nil {
		return "", store.User{}, err
	}
	h, err := s.hash(password)
	if err != nil {
		return "", store.User{}, err
	}
	u, err := s.Store.AddUser(name, h)
	if err != nil {
		return "", store.User{}, err
	}
	_ = os.Remove(filepath.Join(s.Dir, setupFile))
	_ = s.Store.Audit(name, "auth.setup", name, ip)
	tok, err := s.newSession(u.ID, ip, ua)
	return tok, u, err
}

// ---- Logging in ----

// Login checks a name, password and, when two-step login is on, a code.
// Without a code it answers need_code once the password is right.
func (s *Service) Login(ip, ua, name, password, code string) (string, store.User, error) {
	name = strings.TrimSpace(name)
	if err := s.locked(ip, name); err != nil {
		return "", store.User{}, err
	}
	u, err := s.Store.UserByName(name)
	hash := []byte(u.Password)
	if err != nil {
		hash = s.dummyHash()
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || err != nil {
		s.fail(ip, name)
		_ = s.Store.Audit(orDash(name), "auth.fail", orDash(name), ip)
		return "", store.User{}, refuse("bad_login", "用户名或密码不对")
	}
	if u.TOTP {
		if strings.TrimSpace(code) == "" {
			return "", store.User{}, refuse("need_code", "请输入身份验证器 App 里的 6 位验证码")
		}
		key, err := s.totpKey(u.ID, "totp")
		if err != nil || !s.checkCode(u.ID, key, code) {
			s.fail(ip, name)
			_ = s.Store.Audit(name, "auth.fail", name, ip+"（验证码不对）")
			return "", store.User{}, refuse("bad_code", "验证码不对，请看 App 里最新的 6 位数字")
		}
	}
	s.mu.Lock()
	delete(s.byIP, ip)
	if s.goodIP == nil {
		s.goodIP = map[string]time.Time{}
	}
	s.goodIP[ip] = s.Now()
	s.mu.Unlock()
	_ = s.Store.Audit(name, "auth.login", name, ip)
	tok, err := s.newSession(u.ID, ip, ua)
	return tok, u, err
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// locked refuses an address or account that guessed too often. An
// address that logged in within the last 30 days is spared the account's
// lock, so strangers guessing cannot keep the owner out.
func (s *Service) locked(ip, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	check := []*tries{s.byIP[ip]}
	if at, ok := s.goodIP[ip]; !ok || now.Sub(at) > MaxAge {
		check = append(check, s.byUser[name])
	}
	for _, t := range check {
		if t != nil && now.Before(t.until) {
			mins := int(t.until.Sub(now).Minutes()) + 1
			return refuse("locked", "尝试次数太多了，请 %d 分钟后再试", mins)
		}
	}
	return nil
}

// fail counts a wrong try against the address and the account.
func (s *Service) fail(ip, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byIP == nil {
		s.byIP, s.byUser = map[string]*tries{}, map[string]*tries{}
	}
	now := s.Now()
	bump := func(m map[string]*tries, k string, limit int) {
		t := m[k]
		if t == nil || now.Sub(t.first) > window {
			t = &tries{first: now}
			m[k] = t
		}
		t.n++
		if t.n >= limit {
			t.until = now.Add(lockFor)
			t.n, t.first = 0, now
		}
	}
	bump(s.byIP, ip, ipLimit)
	if name != "" {
		bump(s.byUser, name, userLimit)
	}
}

// ---- Sessions ----

func sessionID(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *Service) newSession(uid int64, ip, ua string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	at := s.Now().UTC().Format(time.RFC3339)
	if len(ua) > 200 {
		ua = ua[:200]
	}
	err := s.Store.AddSession(store.Session{ID: sessionID(tok), UserID: uid, CreatedAt: at, SeenAt: at, IP: ip, UA: ua})
	return tok, err
}

// Check returns the account a session cookie belongs to, if it is still
// good, and notes the use.
func (s *Service) Check(token, ip string) (store.User, bool) {
	if token == "" {
		return store.User{}, false
	}
	id := sessionID(token)
	se, err := s.Store.GetSession(id)
	if err != nil {
		return store.User{}, false
	}
	now := s.Now()
	created, _ := time.Parse(time.RFC3339, se.CreatedAt)
	seen, _ := time.Parse(time.RFC3339, se.SeenAt)
	if now.Sub(seen) > IdleTimeout || now.Sub(created) > MaxAge {
		_ = s.Store.DeleteSession(id)
		return store.User{}, false
	}
	u, err := s.Store.GetUser(se.UserID)
	if err != nil {
		return store.User{}, false
	}
	if now.Sub(seen) > time.Minute || se.IP != ip {
		_ = s.Store.TouchSession(id, now.UTC().Format(time.RFC3339), ip)
	}
	return u, true
}

// Logout ends a session.
func (s *Service) Logout(token string) {
	if token != "" {
		_ = s.Store.DeleteSession(sessionID(token))
	}
}

// SessionView is a session as the account page shows it.
type SessionView struct {
	Key       string `json:"key"` // enough of the hash to end it
	CreatedAt string `json:"createdAt"`
	SeenAt    string `json:"seenAt"`
	IP        string `json:"ip"`
	UA        string `json:"ua"`
	Current   bool   `json:"current"`
}

// Sessions lists an account's logins; current is the caller's cookie.
func (s *Service) Sessions(uid int64, current string) ([]SessionView, error) {
	list, err := s.Store.ListSessions(uid)
	if err != nil {
		return nil, err
	}
	cur := sessionID(current)
	out := make([]SessionView, 0, len(list))
	for _, se := range list {
		v := SessionView{Key: se.ID[:16], CreatedAt: se.CreatedAt, SeenAt: se.SeenAt, IP: se.IP, UA: se.UA, Current: se.ID == cur}
		if v.Current { // this browser first
			out = append([]SessionView{v}, out...)
		} else {
			out = append(out, v)
		}
	}
	return out, nil
}

// EndSession logs out one of the account's other sessions by its key.
func (s *Service) EndSession(uid int64, key string) error {
	list, err := s.Store.ListSessions(uid)
	if err != nil {
		return err
	}
	for _, se := range list {
		if len(key) >= 16 && strings.HasPrefix(se.ID, key) {
			return s.Store.DeleteSession(se.ID)
		}
	}
	return refuse("not_found", "找不到这个登录")
}

// Prune removes sessions that ran out.
func (s *Service) Prune() error {
	now := s.Now()
	return s.Store.DeleteStaleSessions(now.Add(-IdleTimeout), now.Add(-MaxAge))
}

// ---- Password ----

// ChangePassword sets a new password after checking the old one, and logs
// out every other session.
func (s *Service) ChangePassword(uid int64, keep, old, pw string) error {
	u, err := s.Store.GetUser(uid)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(old)) != nil {
		return refuse("bad_login", "原密码不对")
	}
	if err := checkPassword(u.Name, pw); err != nil {
		return err
	}
	h, err := s.hash(pw)
	if err != nil {
		return err
	}
	if err := s.Store.SetPassword(uid, h); err != nil {
		return err
	}
	_ = s.Store.Audit(u.Name, "auth.password", u.Name, "改了密码，其他登录已退出")
	return s.Store.DeleteSessions(uid, sessionID(keep))
}

// ResetPassword sets an account's password from the server's command
// line, making the account if there is none, turns two-step login off and
// logs every session out. It is how someone locked out gets back in.
func ResetPassword(st *store.Store, sec secrets.Store, name, pw string) error {
	name = strings.TrimSpace(name)
	if err := checkName(name); err != nil {
		return err
	}
	if err := checkPassword(name, pw); err != nil {
		return err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	if err != nil {
		return err
	}
	u, err := st.UserByName(name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if n, _ := st.CountUsers(); n > 0 {
			return refuse("not_found", "没有这个用户名")
		}
		if u, err = st.AddUser(name, string(h)); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if err := st.SetPassword(u.ID, string(h)); err != nil {
			return err
		}
	}
	_ = st.SetTOTP(u.ID, false)
	_ = sec.Delete(totpName(u.ID, "totp"))
	_ = st.DeleteSessions(u.ID, "")
	_ = st.Audit(name, "auth.reset", name, "在服务器命令行重设了密码，两步验证已关闭")
	return nil
}

// ---- Two-step login (TOTP) ----

func totpName(uid int64, what string) string {
	return "user/" + strconv.FormatInt(uid, 10) + "/" + what
}

func (s *Service) totpKey(uid int64, what string) ([]byte, error) {
	v, err := s.Secrets.Get(totpName(uid, what))
	if err != nil {
		return nil, err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(v)
}

// Code is the TOTP code for a key at a 30-second step.
func Code(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000)
}

// checkCode accepts the code of the current step or its neighbours (the
// phone's clock may be a little off), each at most once.
func (s *Service) checkCode(uid int64, key []byte, code string) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return false
	}
	now := s.Now().Unix() / 30
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastStep == nil {
		s.lastStep = map[int64]int64{}
	}
	for _, step := range []int64{now - 1, now, now + 1} {
		if step <= s.lastStep[uid] {
			continue
		}
		if hmac.Equal([]byte(Code(key, step)), []byte(code)) {
			s.lastStep[uid] = step
			return true
		}
	}
	return false
}

// TOTPSetup is what the page shows to add the account to an app.
type TOTPSetup struct {
	Secret string `json:"secret"` // for typing in by hand
	URI    string `json:"uri"`
	QR     string `json:"qr"` // data: URL of a PNG
}

// BeginTOTP makes a new key, kept aside until a code from it confirms
// the app has it.
func (s *Service) BeginTOTP(uid int64) (TOTPSetup, error) {
	u, err := s.Store.GetUser(uid)
	if err != nil {
		return TOTPSetup{}, err
	}
	key := make([]byte, 20)
	if _, err := rand.Read(key); err != nil {
		return TOTPSetup{}, err
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)
	if err := s.Secrets.Set(totpName(uid, "totp-new"), secret); err != nil {
		return TOTPSetup{}, err
	}
	label := url.PathEscape(issuer + ":" + u.Name)
	uri := "otpauth://totp/" + label + "?secret=" + secret + "&issuer=" + url.QueryEscape(issuer) + "&algorithm=SHA1&digits=6&period=30"
	out := TOTPSetup{Secret: secret, URI: uri}
	if c, err := qr.Encode(uri, qr.M); err == nil {
		c.Scale = 6
		out.QR = "data:image/png;base64," + base64.StdEncoding.EncodeToString(c.PNG())
	}
	return out, nil
}

// EnableTOTP turns two-step login on once a code from the new key checks
// out.
func (s *Service) EnableTOTP(uid int64, code string) error {
	key, err := s.totpKey(uid, "totp-new")
	if err != nil {
		return refuse("not_found", "请先点「开启两步验证」，扫描新的二维码")
	}
	if !s.checkCode(uid, key, code) {
		return refuse("bad_code", "验证码不对。请确认手机时间准确，输入 App 里最新的 6 位数字")
	}
	if err := s.Secrets.Set(totpName(uid, "totp"), base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)); err != nil {
		return err
	}
	_ = s.Secrets.Delete(totpName(uid, "totp-new"))
	if err := s.Store.SetTOTP(uid, true); err != nil {
		return err
	}
	u, _ := s.Store.GetUser(uid)
	_ = s.Store.Audit(u.Name, "auth.totp", u.Name, "开启了两步验证")
	return nil
}

// DisableTOTP turns two-step login off after checking the password.
func (s *Service) DisableTOTP(uid int64, password string) error {
	u, err := s.Store.GetUser(uid)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password)) != nil {
		return refuse("bad_login", "密码不对")
	}
	if err := s.Store.SetTOTP(uid, false); err != nil {
		return err
	}
	_ = s.Secrets.Delete(totpName(uid, "totp"))
	_ = s.Store.Audit(u.Name, "auth.totp", u.Name, "关闭了两步验证")
	return nil
}
