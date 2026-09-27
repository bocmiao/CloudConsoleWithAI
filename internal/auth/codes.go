package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// Logging in with a code sent by email or text message, besides (or, when
// the owner chooses, instead of) the password. A code replaces the
// password only: an account with two-step login still needs the app's
// code after it. Codes are six digits, work once, for ten minutes, and
// five wrong tries throw one away; sending is limited per address, per
// email or phone and per day, and an address that is not bound gets the
// same answer as one that is, so the page cannot be used to find out
// which ones are.

const (
	methodsSetting = "auth.methods"
	codeTTL        = 10 * time.Minute
	codeTries      = 5
	sendGap        = 60 * time.Second // between two codes to one email or phone
	sendPerTarget  = 10               // codes to one email or phone a day
	sendPerIP      = 10               // codes asked for by one address an hour
)

// Methods are the ways of logging in that are turned on.
type Methods struct {
	Password bool `json:"password"`
	Email    bool `json:"email"`
	SMS      bool `json:"sms"`
}

func loadMethods(st *store.Store) Methods {
	m := Methods{Password: true}
	if raw, _ := st.Setting(methodsSetting); raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	return m
}

func saveMethods(st *store.Store, m Methods) error {
	raw, _ := json.Marshal(m)
	return st.SetSetting(methodsSetting, string(raw))
}

// ready says whether a channel can send; no sender means none can.
func (s *Service) ready(channel string) bool {
	return s.Send != nil && s.Ready != nil && s.Ready(channel)
}

// SavedMethods are the switches as the owner set them.
func (s *Service) SavedMethods() Methods { return loadMethods(s.Store) }

// ActiveMethods are the ways that work now: a code method needs its
// sender set up, and when no code method works the password always does,
// so a broken mail setup cannot lock the owner out.
func (s *Service) ActiveMethods() Methods {
	m := loadMethods(s.Store)
	m.Email = m.Email && s.ready("email")
	m.SMS = m.SMS && s.ready("sms")
	if !m.Email && !m.SMS {
		m.Password = true
	}
	return m
}

// SetMethods turns ways of logging in on or off for everyone. A code
// method needs its sender and the account's bound email or phone; the
// password can only go when the account can still log in with a code.
func (s *Service) SetMethods(uid int64, m Methods) error {
	u, err := s.Store.GetUser(uid)
	if err != nil {
		return err
	}
	if m.Email && !s.ready("email") {
		return refuse("not_ready", "先设置发信邮箱（SMTP），才能开启邮箱验证码登录")
	}
	if m.Email && u.Email == "" {
		return refuse("not_bound", "先绑定邮箱，才能开启邮箱验证码登录")
	}
	if m.SMS && !s.ready("sms") {
		return refuse("not_ready", "先设置短信（腾讯云或阿里云），才能开启短信验证码登录")
	}
	if m.SMS && u.Phone == "" {
		return refuse("not_bound", "先绑定手机号，才能开启短信验证码登录")
	}
	if !m.Password && !m.Email && !m.SMS {
		return refuse("no_method", "至少要留一种登录方式")
	}
	if err := saveMethods(s.Store, m); err != nil {
		return err
	}
	var on []string
	for _, x := range []struct {
		ok   bool
		name string
	}{{m.Password, "密码"}, {m.Email, "邮箱验证码"}, {m.SMS, "短信验证码"}} {
		if x.ok {
			on = append(on, x.name)
		}
	}
	_ = s.Store.Audit(u.Name, "auth.methods", u.Name, "登录方式："+strings.Join(on, "、"))
	return nil
}

// ---- Emails and phone numbers ----

var phoneRe = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// NormEmail checks an email address and writes it one way.
func NormEmail(raw string) (string, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil || a.Name != "" || len(a.Address) > 254 || !strings.Contains(a.Address, ".") {
		return "", refuse("bad_target", "邮箱地址不对")
	}
	return strings.ToLower(a.Address), nil
}

// NormPhone checks a phone number and writes it with its country code:
// 13800138000 → +8613800138000.
func NormPhone(raw string) (string, error) {
	p := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(strings.TrimSpace(raw))
	switch {
	case len(p) == 11 && strings.HasPrefix(p, "1"):
		p = "+86" + p
	case strings.HasPrefix(p, "0086"):
		p = "+86" + p[4:]
	case strings.HasPrefix(p, "86") && len(p) == 13:
		p = "+" + p
	}
	if !phoneRe.MatchString(p) {
		return "", refuse("bad_target", "手机号不对（中国大陆号码直接填 11 位，其他地区带 + 和国家代码）")
	}
	return p, nil
}

func normTarget(channel, raw string) (string, error) {
	switch channel {
	case "email":
		return NormEmail(raw)
	case "sms":
		return NormPhone(raw)
	}
	return "", refuse("bad_target", "不支持的方式")
}

// Mask hides most of an email or phone number for logs and pages.
func Mask(target string) string {
	if i := strings.Index(target, "@"); i > 0 {
		keep := min(2, i)
		return target[:keep] + "***" + target[i:]
	}
	if len(target) > 7 {
		return target[:len(target)-8] + "****" + target[len(target)-4:]
	}
	return "***"
}

func (s *Service) userByTarget(channel, target string) (store.User, error) {
	if channel == "email" {
		return s.Store.UserByEmail(target)
	}
	return s.Store.UserByPhone(target)
}

// ---- Codes ----

type pendingCode struct {
	mac     []byte
	uid     int64
	target  string
	expires time.Time
	tries   int
}

func (s *Service) codeMAC(key, code string) []byte {
	s.mu.Lock()
	if s.codeKey == nil {
		s.codeKey = make([]byte, 32)
		_, _ = rand.Read(s.codeKey)
	}
	k := s.codeKey
	s.mu.Unlock()
	m := hmac.New(sha256.New, k)
	m.Write([]byte(key + "|" + code))
	return m.Sum(nil)
}

func newCode() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(b[:])%1000000)
}

// allowSend applies the sending limits; it counts the send when allowed.
// The minute between codes is per purpose (a binding code does not hold
// up the first login); the day's count is per email or phone.
func (s *Service) allowSend(ip, purpose, target string) error {
	now := s.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sends == nil {
		s.sends = map[string][]time.Time{}
	}
	keep := func(list []time.Time, within time.Duration) []time.Time {
		var out []time.Time
		for _, t := range list {
			if now.Sub(t) < within {
				out = append(out, t)
			}
		}
		return out
	}
	byTarget := keep(s.sends["t|"+target], 24*time.Hour)
	gap := keep(s.sends["gap|"+purpose+"|"+target], sendGap)
	byIP := keep(s.sends["ip|"+ip], time.Hour)
	if len(s.sends) > 4096 { // forget the old ones now and then
		for k, v := range s.sends {
			if len(keep(v, 24*time.Hour)) == 0 {
				delete(s.sends, k)
			}
		}
	}
	switch {
	case len(gap) > 0:
		wait := sendGap - now.Sub(gap[len(gap)-1])
		return refuse("too_soon", "验证码刚发过，%d 秒后可以再发", int(wait.Seconds())+1)
	case len(byTarget) >= sendPerTarget:
		return refuse("too_many", "今天给它发的验证码太多了，明天再试，或者用其他方式登录")
	case ip != "" && len(byIP) >= sendPerIP:
		return refuse("too_many", "请求验证码太频繁了，请稍后再试")
	}
	s.sends["t|"+target] = append(byTarget, now)
	s.sends["gap|"+purpose+"|"+target] = []time.Time{now}
	if ip != "" {
		s.sends["ip|"+ip] = append(byIP, now)
	}
	return nil
}

// takeCode checks a code against the one kept under key. The code stays
// when right, so the next step (two-step login) can still use it; five
// wrong tries throw it away.
func (s *Service) checkPending(key string, uid int64, code string) (*pendingCode, bool) {
	code = strings.TrimSpace(code)
	mac := s.codeMAC(key, code)
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.codes[key]
	if p == nil {
		return nil, false
	}
	if s.Now().After(p.expires) {
		delete(s.codes, key)
		return nil, false
	}
	if (uid != 0 && p.uid != uid) || len(code) != 6 || !hmac.Equal(p.mac, mac) {
		p.tries++
		if p.tries >= codeTries {
			delete(s.codes, key)
		}
		return nil, false
	}
	return p, true
}

func (s *Service) putCode(key string, p *pendingCode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.codes == nil {
		s.codes = map[string]*pendingCode{}
	}
	for k, x := range s.codes {
		if s.Now().After(x.expires) {
			delete(s.codes, k)
		}
	}
	s.codes[key] = p
}

func (s *Service) dropCode(key string) {
	s.mu.Lock()
	delete(s.codes, key)
	s.mu.Unlock()
}

func loginKey(channel, target string) string { return "login|" + channel + "|" + target }
func bindKey(uid int64, channel string) string {
	return fmt.Sprintf("bind|%d|%s", uid, channel)
}

var channelName = map[string]string{"email": "邮箱", "sms": "手机"}

// SendLoginCode sends a login code to a bound email or phone. For one
// that is not bound it does nothing and answers the same.
func (s *Service) SendLoginCode(ctx context.Context, ip, channel, raw string) error {
	m := s.ActiveMethods()
	if (channel == "email" && !m.Email) || (channel == "sms" && !m.SMS) || (channel != "email" && channel != "sms") {
		return refuse("method_off", "没有开启%s验证码登录", channelName[channel])
	}
	target, err := normTarget(channel, raw)
	if err != nil {
		return err
	}
	if err := s.allowSend(ip, "login", target); err != nil {
		return err
	}
	u, err := s.userByTarget(channel, target)
	if err != nil {
		_ = s.Store.Audit("-", "auth.code", Mask(target), ip+"（没有绑定这个"+channelName[channel]+"，没有发送）")
		return nil
	}
	code := newCode()
	key := loginKey(channel, target)
	s.putCode(key, &pendingCode{mac: s.codeMAC(key, code), uid: u.ID, target: target, expires: s.Now().Add(codeTTL)})
	if err := s.Send(ctx, channel, target, code, "login", ip); err != nil {
		s.dropCode(key)
		// The reason goes to the log only: it would tell a stranger that
		// this email or phone is bound.
		log.Printf("登录验证码没有发出（%s %s）：%v", channel, Mask(target), err)
		_ = s.Store.Audit(u.Name, "auth.code", Mask(target), ip+"（发送失败："+err.Error()+"）")
		return refuse("send_failed", "验证码没有发出去，请稍后再试或者用其他方式登录（原因记在服务器的运行日志里）")
	}
	_ = s.Store.Audit(u.Name, "auth.code", Mask(target), ip)
	return nil
}

// LoginWithCode logs in with a code sent by SendLoginCode, then the
// app's code when two-step login is on (need_code asks for it; the
// emailed code stays valid meanwhile).
func (s *Service) LoginWithCode(ip, ua, channel, raw, code, totp string) (string, store.User, error) {
	m := s.ActiveMethods()
	if (channel == "email" && !m.Email) || (channel == "sms" && !m.SMS) || (channel != "email" && channel != "sms") {
		return "", store.User{}, refuse("method_off", "没有开启%s验证码登录", channelName[channel])
	}
	target, err := normTarget(channel, raw)
	if err != nil {
		return "", store.User{}, err
	}
	u, uerr := s.userByTarget(channel, target)
	done, err := s.attempt(ip, u.Name)
	if err != nil {
		return "", store.User{}, err
	}
	key := loginKey(channel, target)
	if _, ok := s.checkPending(key, u.ID, code); !ok || uerr != nil {
		_ = s.Store.Audit(orDash(u.Name), "auth.fail", Mask(target), ip+"（验证码不对或过期）")
		return "", store.User{}, refuse("bad_login", "验证码不对或已经过期，可以重新获取")
	}
	if u.TOTP {
		if strings.TrimSpace(totp) == "" {
			done()
			return "", store.User{}, refuse("need_code", "请输入身份验证器 App 里的 6 位验证码")
		}
		key2, err := s.totpKey(u.ID, "totp")
		if err != nil || !s.checkCode(u.ID, key2, totp) {
			_ = s.Store.Audit(u.Name, "auth.fail", u.Name, ip+"（两步验证码不对）")
			return "", store.User{}, refuse("bad_code", "验证码不对，请看 App 里最新的 6 位数字")
		}
	}
	s.dropCode(key)
	done()
	s.mu.Lock()
	delete(s.byIP, ip)
	if s.goodIP == nil {
		s.goodIP = map[string]time.Time{}
	}
	s.goodIP[u.Name+"|"+ip] = s.Now()
	s.mu.Unlock()
	_ = s.Store.Audit(u.Name, "auth.login", u.Name, ip)
	tok, err := s.newSession(u.ID, ip, ua)
	return tok, u, err
}

// ---- Binding ----

// Confirm checks a logged-in account's password before a sensitive
// change (binding, where codes are sent from), counting wrong ones.
func (s *Service) Confirm(uid int64, ip, password string) error {
	u, err := s.Store.GetUser(uid)
	if err != nil {
		return err
	}
	return s.checkOwn(ip, u, password, "密码不对")
}

// BeginBind sends a code to the email or phone to bind, after the
// password: whoever controls where codes go controls the account.
func (s *Service) BeginBind(ctx context.Context, uid int64, ip, channel, raw, password string) error {
	if err := s.Confirm(uid, ip, password); err != nil {
		return err
	}
	if !s.ready(channel) {
		return refuse("not_ready", "先设置%s", map[string]string{"email": "发信邮箱（SMTP）", "sms": "短信（腾讯云或阿里云）"}[channel])
	}
	target, err := normTarget(channel, raw)
	if err != nil {
		return err
	}
	if other, err := s.userByTarget(channel, target); err == nil {
		if other.ID == uid {
			return refuse("same", "已经绑定了这个%s", channelName[channel])
		}
		return refuse("taken", "这个%s已经绑定了别的账号", channelName[channel])
	}
	if err := s.allowSend(ip, "bind", target); err != nil {
		return err
	}
	code := newCode()
	key := bindKey(uid, channel)
	s.putCode(key, &pendingCode{mac: s.codeMAC(key, code), uid: uid, target: target, expires: s.Now().Add(codeTTL)})
	if err := s.Send(ctx, channel, target, code, "bind", ip); err != nil {
		s.dropCode(key)
		return err
	}
	return nil
}

// ConfirmBind binds the email or phone the code was sent to.
func (s *Service) ConfirmBind(uid int64, ip, channel, code string) (string, error) {
	u, err := s.Store.GetUser(uid)
	if err != nil {
		return "", err
	}
	done, err := s.attempt(ip, u.Name)
	if err != nil {
		return "", err
	}
	key := bindKey(uid, channel)
	p, ok := s.checkPending(key, uid, code)
	if !ok {
		return "", refuse("bad_code", "验证码不对或已经过期")
	}
	field := map[string]string{"email": "email", "sms": "phone"}[channel]
	if err := s.Store.SetContact(uid, field, p.target); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", refuse("taken", "这个%s已经绑定了别的账号", channelName[channel])
		}
		return "", err
	}
	s.dropCode(key)
	done()
	_ = s.Store.Audit(u.Name, "auth.bind", u.Name, "绑定了"+channelName[channel]+" "+Mask(p.target))
	return p.target, nil
}

// Unbind removes the account's email or phone, and turns its code login
// off; not when it is the only way left to log in.
func (s *Service) Unbind(uid int64, ip, channel, password string) error {
	if err := s.Confirm(uid, ip, password); err != nil {
		return err
	}
	u, _ := s.Store.GetUser(uid)
	m := loadMethods(s.Store)
	switch channel {
	case "email":
		m.Email = false
	case "sms":
		m.SMS = false
	default:
		return errors.New("unknown channel")
	}
	if !m.Password && !m.Email && !m.SMS {
		return refuse("no_method", "这是现在唯一的登录方式，请先开启密码登录或另一种验证码登录")
	}
	if err := saveMethods(s.Store, m); err != nil {
		return err
	}
	if err := s.Store.SetContact(uid, map[string]string{"email": "email", "sms": "phone"}[channel], ""); err != nil {
		return err
	}
	_ = s.Store.Audit(u.Name, "auth.bind", u.Name, "解绑了"+channelName[channel])
	return nil
}

// findLogin finds the account a password login names: its name, or the
// email or phone number bound to it.
func (s *Service) findLogin(name string) (store.User, error) {
	u, err := s.Store.UserByName(name)
	if err == nil {
		return u, nil
	}
	if strings.Contains(name, "@") {
		if e, err := NormEmail(name); err == nil {
			return s.Store.UserByEmail(e)
		}
	}
	if p, err := NormPhone(name); err == nil {
		return s.Store.UserByPhone(p)
	}
	return store.User{}, store.ErrNotFound
}
