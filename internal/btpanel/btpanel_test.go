package btpanel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/btpanel/btpaneltest"
)

var ctx = context.Background()

// setup starts a fake panel and a client that reaches it the way Miao
// Panel reaches a real one.
func setup(t *testing.T) (*btpaneltest.Fake, *Client) {
	t.Helper()
	f := btpaneltest.New()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, New(btpaneltest.Transport(srv.URL), 8888, f.Key, "http")
}

func reason(t *testing.T, err error, want Reason) *Error {
	t.Helper()
	var pe *Error
	if !errors.As(err, &pe) || pe.Reason != want {
		t.Fatalf("err = %v (%#v), want reason %d", err, pe, want)
	}
	return pe
}

// lastForm is the form of the last request the fake accepted for action.
func lastForm(f *btpaneltest.Fake, action string) url.Values {
	f.Lock()
	defer f.Unlock()
	for i := len(f.Requests) - 1; i >= 0; i-- {
		if f.Requests[i].Action == action {
			return f.Requests[i].Form
		}
	}
	return nil
}

func count(f *btpaneltest.Fake, action string) int {
	f.Lock()
	defer f.Unlock()
	n := 0
	for _, r := range f.Requests {
		if r.Action == action {
			n++
		}
	}
	return n
}

func TestTokenIsPanelFormula(t *testing.T) {
	c := New(nil, 8888, " abc123 ", "")
	c.now = func() time.Time { return time.Unix(1700000000, 0) }
	form := url.Values{}
	c.sign(form)
	// md5("1700000000" + md5("abc123")), worked out with md5sum.
	if form.Get("request_time") != "1700000000" || form.Get("request_token") != "9f27c201f40a4ada12b39a65138ad643" {
		t.Fatalf("signed form = %v", form)
	}
	if c.Scheme != "http" {
		t.Fatalf("default scheme = %q", c.Scheme)
	}
}

func TestProbe(t *testing.T) {
	_, c := setup(t)
	var traced []string
	c.Trace = func(method, path string, body []byte) { traced = append(traced, method+" "+path+" "+string(body)) }
	info, err := c.Probe(ctx)
	if err != nil || info.Version != "11.8.0" || info.CPUs != 2 || info.MemTotalMB != 1987 || !strings.Contains(info.System, "Ubuntu") {
		t.Fatalf("probe = %+v, %v", info, err)
	}
	if len(traced) != 1 || traced[0] != "POST /system?action=GetSystemTotal " {
		t.Fatalf("trace = %q", traced)
	}
}

func TestBadKeyIsExplained(t *testing.T) {
	f, _ := setup(t)
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := New(btpaneltest.Transport(srv.URL), 8888, "wrong-key", "http")
	_, err := c.Probe(ctx)
	reason(t, err, ReasonBadKey)
	if !strings.Contains(err.Error(), "API 密钥不对") || c.Scheme != "http" {
		t.Fatalf("err = %v, scheme %s", err, c.Scheme)
	}
	// The panel locks the address out after 20 failures in a row.
	for i := 0; i < 19; i++ {
		_, _ = c.Probe(ctx)
	}
	_, err = c.Probe(ctx)
	reason(t, err, ReasonLockedOut)
	if !strings.Contains(err.Error(), "1 小时") {
		t.Fatalf("err = %v", err)
	}
}

func TestAPIOffAndWhitelist(t *testing.T) {
	f, c := setup(t)
	f.Lock()
	f.APIEnabled = false
	f.Unlock()
	_, err := c.Probe(ctx)
	reason(t, err, ReasonNotFound)
	if !strings.Contains(err.Error(), "面板设置 → API 接口") || !strings.Contains(err.Error(), "127.0.0.1") || c.Scheme != "http" {
		t.Fatalf("err = %v, scheme %s", err, c.Scheme)
	}

	f.Lock()
	f.APIEnabled, f.AllowedIPs = true, []string{"203.0.113.9"}
	f.Unlock()
	_, err = c.Probe(ctx)
	reason(t, err, ReasonIPNotAllowed)
	if !strings.Contains(err.Error(), "IP 白名单里没有 127.0.0.1") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownActionAndMissingField(t *testing.T) {
	f, c := setup(t)
	_, err := c.call(ctx, "site", "NoSuchAction", nil)
	pe := reason(t, err, ReasonUnknownAction)
	if pe.Message != "指定参数无效!" || !strings.Contains(err.Error(), "site/NoSuchAction") {
		t.Fatalf("err = %v", err)
	}
	// A field the panel reads without checking makes it raise, and API
	// callers then get its 404 page.
	_, err = c.call(ctx, "site", "AddSite", url.Values{"path": {"/www/wwwroot/x"}})
	reason(t, err, ReasonNotFound)
	if !strings.Contains(err.Error(), "site/AddSite") {
		t.Fatalf("err = %v", err)
	}
	f.Lock()
	crashes := f.Crashes
	f.Unlock()
	if len(crashes) != 1 || !strings.Contains(crashes[0], "webname") {
		t.Fatalf("crashes = %v", crashes)
	}
}

func TestOddAnswers(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   Reason
	}{
		{http.StatusFound, "", ReasonNotFound},      // sent to the login page
		{http.StatusUnauthorized, "", ReasonBadKey}, // BasicAuth after a failed API check
		{http.StatusOK, "<html>hello</html>", ReasonBadAnswer},
		{http.StatusBadGateway, "bad gateway", ReasonBadAnswer},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.status == http.StatusFound {
				w.Header().Set("Location", "/login")
			}
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		c := New(btpaneltest.Transport(srv.URL), 8888, "k", "http")
		_, err := c.systemTotal(ctx)
		reason(t, err, tc.want)
		srv.Close()
	}
}

func TestProbeSwitchesScheme(t *testing.T) {
	f := btpaneltest.New()
	tlsSrv := httptest.NewUnstartedServer(f)
	tlsSrv.Config.ErrorLog = log.New(io.Discard, "", 0) // the plain request is expected
	tlsSrv.StartTLS()
	defer tlsSrv.Close()
	c := New(btpaneltest.Transport(tlsSrv.URL), 8888, f.Key, "http")
	if info, err := c.Probe(ctx); err != nil || c.Scheme != "https" || info.Version == "" {
		t.Fatalf("probe over TLS: %+v %v scheme=%s", info, err, c.Scheme)
	}

	plain := httptest.NewServer(f)
	defer plain.Close()
	c = New(btpaneltest.Transport(plain.URL), 8888, f.Key, "https")
	if _, err := c.Probe(ctx); err != nil || c.Scheme != "http" {
		t.Fatalf("probe over plain HTTP: %v scheme=%s", err, c.Scheme)
	}
	// Plain HTTP spoken to HTTPS is explained on ordinary calls too.
	c.Scheme = "https"
	_, err := c.Sites(ctx)
	if err == nil || !strings.Contains(err.Error(), "请改用 http") {
		t.Fatalf("err = %v", err)
	}

	closed := httptest.NewServer(f)
	closed.Close()
	c = New(btpaneltest.Transport(closed.URL), 8888, f.Key, "http")
	if _, err := c.Probe(ctx); err == nil || !strings.Contains(err.Error(), "连接宝塔面板") || c.Scheme != "http" {
		t.Fatalf("closed port: %v scheme=%s", err, c.Scheme)
	}
}

func TestSitesArePaged(t *testing.T) {
	f, c := setup(t)
	for i := 1; i <= 45; i++ {
		f.AddSite(fmt.Sprintf("s%02d.example.com", i))
	}
	stopped := f.Site("s07.example.com")
	f.Lock()
	stopped.Running, stopped.PS, stopped.PHP = false, "a & <b>", "00"
	f.Unlock()

	sites, err := c.Sites(ctx)
	if err != nil || len(sites) != 45 {
		t.Fatalf("sites: %d %v", len(sites), err)
	}
	if n := count(f, "data/getData"); n != 3 {
		t.Fatalf("getData calls = %d, want 3 pages of 20", n)
	}
	// The user's own page size is used and left alone.
	if form := lastForm(f, "data/getData"); form.Has("limit") || form.Get("p") != "3" {
		t.Fatalf("last page form = %v", form)
	}
	f.Lock()
	size := f.PageSize
	f.Unlock()
	if size != 20 {
		t.Fatalf("page size changed to %d", size)
	}

	s, err := c.Site(ctx, "s07.example.com")
	if err != nil || s.Running || s.Remark != "a & <b>" || s.PHPVersion != "静态" || s.Path != "/www/wwwroot/s07.example.com" ||
		s.Expires != "0000-00-00" || s.DomainCount != 1 || s.ProjectType != "PHP" || s.Cert != nil || s.Title != s.Name {
		t.Fatalf("site = %+v, %v", s, err)
	}
	if s, err := c.Site(ctx, "s08.example.com"); err != nil || !s.Running || s.PHPVersion != "8.2" {
		t.Fatalf("site = %+v, %v", s, err)
	}
	if _, err := c.Site(ctx, "nope.example.com"); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "nope.example.com") {
		t.Fatalf("missing site: %v", err)
	}
}

func TestCreateStartStopDeleteSite(t *testing.T) {
	f, c := setup(t)
	s, err := c.CreateSite(ctx, NewSite{Domain: "blog.example.com", Aliases: []string{"www.example.com", "alt.example.com:8080"}, PHP: "82", Remark: "博客"})
	if err != nil || s.Name != "blog.example.com" || s.Path != "/www/wwwroot/blog.example.com" || s.DomainCount != 3 || s.PHPVersion != "8.2" || s.Remark != "博客" {
		t.Fatalf("created %+v, %v", s, err)
	}
	form := lastForm(f, "site/AddSite")
	if form.Get("webname") != `{"count":2,"domain":"blog.example.com","domainlist":["www.example.com","alt.example.com:8080"]}` ||
		form.Get("port") != "80" || form.Get("ftp") != "false" || form.Get("sql") != "false" || form.Get("version") != "82" {
		t.Fatalf("AddSite form = %v", form)
	}

	// A static site on another port, with no aliases: domainlist must
	// still be a list.
	s2, err := c.CreateSite(ctx, NewSite{Domain: "static.example.com:8081", Path: "/data/static"})
	if err != nil || s2.PHPVersion != "静态" || s2.Path != "/data/static" {
		t.Fatalf("static site %+v, %v", s2, err)
	}
	if form := lastForm(f, "site/AddSite"); !strings.Contains(form.Get("webname"), `"domainlist":[]`) || form.Get("port") != "8081" || form.Get("version") != "00" {
		t.Fatalf("AddSite form = %v", form)
	}
	if _, err := c.CreateSite(ctx, NewSite{Domain: "new.example.com", PHP: "56"}); err == nil || !strings.Contains(err.Error(), "指定PHP版本不存在") {
		t.Fatalf("missing PHP: %v", err)
	}
	if _, err := c.CreateSite(ctx, NewSite{Domain: "blog.example.com"}); err == nil || !strings.Contains(err.Error(), "您添加的域名已存在") {
		t.Fatalf("taken domain: %v", err)
	}

	if err := c.SetSiteRunning(ctx, s, false); err != nil || f.Site(s.Name).Running {
		t.Fatalf("stop: %v", err)
	}
	if form := lastForm(f, "site/SiteStop"); form.Get("id") != fmt.Sprint(s.ID) || form.Get("name") != s.Name {
		t.Fatalf("SiteStop form = %v", form)
	}
	if err := c.SetSiteRunning(ctx, s, true); err != nil || !f.Site(s.Name).Running {
		t.Fatalf("start: %v", err)
	}

	if err := c.DeleteSite(ctx, s, DeleteSiteOptions{}); err != nil || f.Site(s.Name) != nil {
		t.Fatalf("delete: %v", err)
	}
	form = lastForm(f, "site/DeleteSite")
	if form.Has("path") || form.Has("database") || form.Has("ftp") || form.Get("webname") != s.Name {
		t.Fatalf("DeleteSite form = %v", form)
	}
	f.Lock()
	_, kept := f.Files["/www/wwwroot/blog.example.com/index.html"]
	f.Unlock()
	if !kept {
		t.Fatal("the site's files went by default")
	}
	if err := c.DeleteSite(ctx, s2, DeleteSiteOptions{Files: true, Database: true}); err != nil {
		t.Fatal(err)
	}
	if form := lastForm(f, "site/DeleteSite"); form.Get("path") != "1" || form.Get("database") != "1" || form.Has("ftp") {
		t.Fatalf("DeleteSite form = %v", form)
	}
	f.Lock()
	_, kept = f.Files["/data/static/index.html"]
	f.Unlock()
	if kept {
		t.Fatal("files asked to go were kept")
	}
	if err := c.DeleteSite(ctx, s2, DeleteSiteOptions{}); err == nil || !strings.Contains(err.Error(), "指定站点不存在") {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestDomains(t *testing.T) {
	f, c := setup(t)
	f.AddSite("a.example.com")
	s, _ := c.Site(ctx, "a.example.com")
	other := f.AddSite("b.example.com")

	if err := c.AddDomains(ctx, s, "www.example.com", "api.example.com:8080"); err != nil {
		t.Fatal(err)
	}
	if form := lastForm(f, "site/AddDomain"); form.Get("domain") != "www.example.com,api.example.com:8080" || form.Get("webname") != s.Name || form.Get("id") != fmt.Sprint(s.ID) {
		t.Fatalf("AddDomain form = %v", form)
	}
	list, err := c.Domains(ctx, s)
	if err != nil || len(list) != 3 || list[2].Name != "api.example.com" || list[2].Port != 8080 || list[2].SiteID != s.ID {
		t.Fatalf("domains = %+v, %v", list, err)
	}
	f.Lock()
	conf := f.Files["/www/server/panel/vhost/nginx/a.example.com.conf"]
	f.Unlock()
	if !strings.Contains(conf, "server_name a.example.com www.example.com api.example.com;") {
		t.Fatalf("server_name not updated:\n%s", conf)
	}

	// The panel adds what it can and reports the rest per domain.
	err = c.AddDomains(ctx, s, "b.example.com", "ok.example.com", "bad_domain")
	pe := reason(t, err, ReasonRefused)
	if !strings.Contains(pe.Message, "已被网站[b.example.com]绑定过了") || !strings.Contains(pe.Message, "bad_domain：域名格式不正确") {
		t.Fatalf("message = %q", pe.Message)
	}
	if list, _ := c.Domains(ctx, s); len(list) != 4 {
		t.Fatalf("ok.example.com was not added: %+v", list)
	}

	if err := c.DeleteDomain(ctx, s, list[1]); err != nil {
		t.Fatal(err)
	}
	if form := lastForm(f, "site/DelDomain"); form.Get("domain") != "www.example.com" || form.Get("port") != "80" || form.Get("webname") != s.Name {
		t.Fatalf("DelDomain form = %v", form)
	}
	b, _ := c.Site(ctx, other.Name)
	bd, _ := c.Domains(ctx, b)
	if err := c.DeleteDomain(ctx, b, bd[0]); err == nil || !strings.Contains(err.Error(), "最后一个域名不能删除") {
		t.Fatalf("last domain: %v", err)
	}
}

func TestSSL(t *testing.T) {
	f, c := setup(t)
	f.AddSite("shop.example.com")
	s, _ := c.Site(ctx, "shop.example.com")
	var traced strings.Builder
	c.Trace = func(method, path string, body []byte) { traced.WriteString(path + " " + string(body) + "\n") }

	st, err := c.SSL(ctx, s)
	if err != nil || st.Enabled || st.Cert != nil || st.Type != -1 || len(st.Domains) != 1 {
		t.Fatalf("ssl before = %+v, %v", st, err)
	}
	if err := c.SetForceHTTPS(ctx, s, true); err == nil || !strings.Contains(err.Error(), "当前未开启SSL") {
		t.Fatalf("force without ssl: %v", err)
	}

	cert, key := btpaneltest.NewCert("shop.example.com", "www.shop.example.com")
	_, otherKey := btpaneltest.NewCert("shop.example.com")
	if err := c.SetSSL(ctx, s, otherKey, cert); err == nil || !strings.Contains(err.Error(), "密钥和证书不匹配") {
		t.Fatalf("mismatched key: %v", err)
	}
	if err := c.SetSSL(ctx, s, key, cert); err != nil {
		t.Fatal(err)
	}
	st, err = c.SSL(ctx, s)
	if err != nil || !st.Enabled || st.ForceHTTPS || st.Type != 0 || st.Cert == nil || st.Cert.Subject != "shop.example.com" ||
		st.Cert.Issuer != "shop.example.com" || len(st.Cert.Domains) != 2 || st.Cert.DaysLeft < 360 || st.Cert.NotAfter != time.Now().Add(365*24*time.Hour).Format("2006-01-02") {
		t.Fatalf("ssl after = %+v %+v, %v", st, st.Cert, err)
	}
	if strings.Contains(fmt.Sprintf("%+v %+v", st, *st.Cert), "PRIVATE KEY") {
		t.Fatal("the private key came through")
	}
	if listed, _ := c.Site(ctx, s.Name); listed.Cert == nil || listed.Cert.NotAfter != st.Cert.NotAfter {
		t.Fatalf("site list cert = %+v", listed.Cert)
	}

	if err := c.SetForceHTTPS(ctx, s, true); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.SSL(ctx, s); !st.ForceHTTPS {
		t.Fatal("force HTTPS did not stick")
	}
	if err := c.SetForceHTTPS(ctx, s, false); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.SSL(ctx, s); st.ForceHTTPS || !st.Enabled {
		t.Fatalf("after unforcing: %+v", st)
	}
	_ = c.SetForceHTTPS(ctx, s, true)
	if err := c.CloseSSL(ctx, s); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.SSL(ctx, s); st.Enabled || st.ForceHTTPS {
		t.Fatalf("after close: %+v", st)
	}
	if strings.Contains(traced.String(), "PRIVATE") || !strings.Contains(traced.String(), "key=%2A%2A%2A") {
		t.Fatalf("trace shows the key:\n%s", traced.String())
	}
}

func TestApplyLetsEncrypt(t *testing.T) {
	f, c := setup(t)
	f.AddSite("le.example.com")
	s, _ := c.Site(ctx, "le.example.com")
	_ = c.AddDomains(ctx, s, "www.le.example.com")

	st, err := c.ApplyLetsEncrypt(ctx, s, nil)
	if err != nil || !st.Enabled || st.Type != 1 || st.Cert == nil || st.Cert.IssuerOrg != "Let's Encrypt" || len(st.Cert.Domains) != 2 {
		t.Fatalf("applied: %+v %+v, %v", st, st.Cert, err)
	}
	form := lastForm(f, "acme/apply_cert_api")
	if form.Get("domains") != `["le.example.com","www.le.example.com"]` || form.Get("auth_type") != "http" || form.Get("auth_to") != fmt.Sprint(s.ID) {
		t.Fatalf("apply form = %v", form)
	}

	// A domain the site does not serve fails validation; the panel's own
	// summary is what the user sees.
	_, err = c.ApplyLetsEncrypt(ctx, s, []string{"other.example.com"})
	pe := reason(t, err, ReasonRefused)
	if pe.Message != "验证失败,域名解析错误或验证URL无法被访问!" {
		t.Fatalf("message = %q", pe.Message)
	}
	_ = c.SetSiteRunning(ctx, s, false)
	if _, err := c.ApplyLetsEncrypt(ctx, s, nil); err == nil || !strings.Contains(err.Error(), "当前网站未开启不能使用文件验证") {
		t.Fatalf("stopped site: %v", err)
	}
}

func TestProxies(t *testing.T) {
	f, c := setup(t)
	f.AddSite("app.example.com")
	s, _ := c.Site(ctx, "app.example.com")

	if err := c.CreateProxy(ctx, s, Proxy{Name: "other", Target: "127.0.0.1:9000"}); err == nil || !strings.Contains(err.Error(), "域名格式错误") {
		t.Fatalf("bad target: %v", err)
	}
	if err := c.CreateProxy(ctx, s, Proxy{Name: "backend", Target: "http://127.0.0.1:8080", Replace: []Replace{{From: "http://", To: "https://"}}}); err != nil {
		t.Fatal(err)
	}
	form := lastForm(f, "site/CreateProxy")
	want := url.Values{"proxyname": {"backend"}, "sitename": {"app.example.com"}, "proxydir": {"/"}, "proxysite": {"http://127.0.0.1:8080"},
		"todomain": {"$host"}, "type": {"1"}, "cache": {"0"}, "cachetime": {"1"}, "advanced": {"0"},
		"subfilter": {`[{"sub1":"http://","sub2":"https://"}]`}}
	if form.Encode() != want.Encode() {
		t.Fatalf("CreateProxy form = %v", form)
	}
	// A proxy for the whole site turns PHP off.
	if s2, _ := c.Site(ctx, s.Name); s2.PHPVersion != "静态" {
		t.Fatalf("php = %q", s2.PHPVersion)
	}
	list, err := c.Proxies(ctx, s)
	if err != nil || len(list) != 1 || list[0].Name != "backend" || !list[0].Enabled || list[0].Dir != "/" || list[0].Host != "$host" ||
		len(list[0].Replace) != 1 || list[0].Replace[0].To != "https://" || list[0].CacheMinutes != 1 {
		t.Fatalf("proxies = %+v, %v", list, err)
	}

	if err := c.CreateProxy(ctx, s, Proxy{Name: "api", Dir: "/api", Target: "http://127.0.0.1:9000"}); err == nil || !strings.Contains(err.Error(), "不能同时设置目录代理和全局代理") {
		t.Fatalf("mixed proxies: %v", err)
	}
	if err := c.CreateProxy(ctx, s, Proxy{Name: "x", Target: "http://127.0.0.1:9000"}); err == nil || !strings.Contains(err.Error(), "名称必须大于3小于40个字符串") {
		t.Fatalf("short name: %v", err)
	}
	if err := c.CreateProxy(ctx, s, Proxy{Name: "second", Target: "http://127.0.0.1:9000"}); err == nil || !strings.Contains(err.Error(), "指定反向代理名称或代理文件夹已存在") {
		t.Fatalf("second proxy for /: %v", err)
	}

	p := list[0]
	p.Target, p.Cache, p.CacheMinutes = "http://127.0.0.1:8081", true, 5
	if err := c.ModifyProxy(ctx, s, p); err != nil {
		t.Fatal(err)
	}
	if list, _ := c.Proxies(ctx, s); list[0].Target != "http://127.0.0.1:8081" || !list[0].Cache || list[0].CacheMinutes != 5 {
		t.Fatalf("modified = %+v", list[0])
	}
	p.Enabled = false
	if err := c.ModifyProxy(ctx, s, p); err != nil {
		t.Fatal(err)
	}
	if list, _ := c.Proxies(ctx, s); list[0].Enabled {
		t.Fatal("proxy not paused")
	}
	// (For "/" the panel would first say that path is taken.)
	if err := c.ModifyProxy(ctx, s, Proxy{Name: "ghost", Dir: "/ghost", Target: "http://127.0.0.1:1"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("modify missing: %v", err)
	}
	if err := c.RemoveProxy(ctx, s, "backend"); err != nil {
		t.Fatal(err)
	}
	if list, _ := c.Proxies(ctx, s); len(list) != 0 {
		t.Fatalf("left = %+v", list)
	}
	if err := c.RemoveProxy(ctx, s, "backend"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
}

func TestRewrite(t *testing.T) {
	f, c := setup(t)
	f.AddSite("wp.example.com")
	s, _ := c.Site(ctx, "wp.example.com")

	names, err := c.RewriteTemplates(ctx, s)
	if err != nil || strings.Join(names, ",") != "default,laravel5,thinkphp,wordpress" {
		t.Fatalf("templates = %v, %v", names, err)
	}
	tpl, err := c.RewriteTemplate(ctx, s, "wordpress")
	if err != nil || !strings.Contains(tpl, "try_files $uri $uri/ /index.php?$args;") {
		t.Fatalf("template = %q, %v", tpl, err)
	}
	if lastForm(f, "files/GetFileBody").Get("path") != "/www/server/panel/rewrite/nginx/wordpress.conf" {
		t.Fatalf("read %v", lastForm(f, "files/GetFileBody"))
	}
	reads := count(f, "files/GetFileBody")
	if _, err := c.RewriteTemplate(ctx, s, "nope"); !errors.Is(err, ErrNotFound) || count(f, "files/GetFileBody") != reads {
		t.Fatalf("unknown template: %v", err)
	}

	cur, err := c.Rewrite(ctx, s)
	if err != nil || cur.Path != "/www/server/panel/vhost/rewrite/wp.example.com.conf" || cur.Content != "" || cur.ModTime == "" {
		t.Fatalf("rewrite = %+v, %v", cur, err)
	}
	if err := c.SetRewrite(ctx, s, tpl, cur.ModTime); err != nil {
		t.Fatal(err)
	}
	if form := lastForm(f, "files/SaveFileBody"); form.Get("encoding") != "utf-8" || form.Get("st_mtime") != cur.ModTime || form.Get("path") != cur.Path {
		t.Fatalf("SaveFileBody form = %v", form)
	}
	if got, _ := c.Rewrite(ctx, s); got.Content != tpl {
		t.Fatalf("rewrite now %q", got.Content)
	}
	err = c.SetRewrite(ctx, s, "location / { "+btpaneltest.BadDirective+" on; }", "")
	pe := reason(t, err, ReasonConfigRejected)
	if !strings.HasPrefix(pe.Message, "nginx: [emerg] unknown directive") || strings.Contains(pe.Message, "<") || !strings.Contains(err.Error(), "恢复了原来的文件") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := c.Rewrite(ctx, s); got.Content != tpl {
		t.Fatalf("broken rules were kept: %q", got.Content)
	}
}

// TestApacheAndAaPanelAnswers covers answers the fake (宝塔 with nginx)
// does not give: Apache's rewrite rules live in the site's .htaccess, and
// aaPanel reports a failed config test as "ERROR: ..." without conf_check.
func TestApacheAndAaPanelAnswers(t *testing.T) {
	f := btpaneltest.New()
	f.AddSite("ap.example.com")
	f.WriteFile("/www/wwwroot/ap.example.com/.htaccess", "RewriteEngine on\n")
	f.WriteFile("/www/server/panel/rewrite/apache/wordpress.conf", "RewriteRule . /index.php [L]\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "GetRewriteList":
			_, _ = io.WriteString(w, `{"rewrite": ["0.当前", "wordpress"], "sitePath": "/www/wwwroot/ap.example.com"}`)
			return
		case "SaveFileBody":
			_, _ = io.WriteString(w, `{"status": false, "msg": "ERROR:<br><font style=\"color:red;\">AH00526: Syntax error on line 2</font>"}`)
			return
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := New(btpaneltest.Transport(srv.URL), 8888, f.Key, "http")
	s, _ := c.Site(ctx, "ap.example.com")

	cur, err := c.Rewrite(ctx, s)
	if err != nil || cur.Path != "/www/wwwroot/ap.example.com/.htaccess" || cur.Content != "RewriteEngine on\n" {
		t.Fatalf("rewrite = %+v, %v", cur, err)
	}
	if tpl, err := c.RewriteTemplate(ctx, s, "wordpress"); err != nil || tpl != "RewriteRule . /index.php [L]\n" {
		t.Fatalf("template = %q, %v", tpl, err)
	}
	pe := reason(t, c.SetRewrite(ctx, s, "Bad line", ""), ReasonConfigRejected)
	if pe.Message != "AH00526: Syntax error on line 2" {
		t.Fatalf("message = %q", pe.Message)
	}
}

func TestNginxConf(t *testing.T) {
	f, c := setup(t)
	f.AddSite("conf.example.com")
	s, _ := c.Site(ctx, "conf.example.com")

	conf, err := c.NginxConf(ctx, s)
	if err != nil || conf.Path != "/www/server/panel/vhost/nginx/conf.example.com.conf" || !strings.Contains(conf.Content, "server_name conf.example.com;") || conf.ReadOnly {
		t.Fatalf("conf = %+v, %v", conf, err)
	}
	next := strings.Replace(conf.Content, "index index.php", "client_max_body_size 50m;\n    index index.php", 1)
	if err := c.SetNginxConf(ctx, s, next, conf.ModTime); err != nil {
		t.Fatal(err)
	}
	now, _ := c.NginxConf(ctx, s)
	if now.Content != next || now.ModTime == conf.ModTime {
		t.Fatalf("saved %+v", now)
	}
	// Written from an old read: the panel refuses rather than overwrite.
	if err := c.SetNginxConf(ctx, s, conf.Content, conf.ModTime); err == nil || !strings.Contains(err.Error(), "文件发生改变") {
		t.Fatalf("stale write: %v", err)
	}
	f.WriteFile(now.Path, now.Content+"\n# edited by hand\n")
	if err := c.SetNginxConf(ctx, s, next, now.ModTime); err == nil {
		t.Fatal("a file changed by someone else was overwritten")
	}

	// A config nginx rejects is put back.
	cur, _ := c.NginxConf(ctx, s)
	bad := strings.Replace(cur.Content, "index index.php", btpaneltest.BadDirective+" on;\n    index index.php", 1)
	err = c.SetNginxConf(ctx, s, bad, cur.ModTime)
	reason(t, err, ReasonConfigRejected)
	if after, _ := c.NginxConf(ctx, s); after.Content != cur.Content || after.ModTime != cur.ModTime {
		t.Fatal("broken config was left in place")
	}
	// The panel edits the SSL block by its 404 marker line and keeps it.
	noMarker := strings.Replace(cur.Content, "#error_page 404/404.html;", "", 1)
	if err := c.SetNginxConf(ctx, s, noMarker, ""); err == nil || !strings.Contains(err.Error(), "请勿修改SSL相关配置中注释的404规则") {
		t.Fatalf("marker removal: %v", err)
	}

	if err := c.TestNginx(ctx); err != nil {
		t.Fatal(err)
	}
	// Without /etc/init.d/nginx the panel saves without testing, and only
	// TestNginx notices.
	f.Lock()
	f.NginxInit, f.NginxRunning = false, false
	f.Unlock()
	if err := c.SetNginxConf(ctx, s, bad, ""); err != nil {
		t.Fatalf("untested save: %v", err)
	}
	err = c.TestNginx(ctx)
	pe := reason(t, err, ReasonConfigInvalid)
	if !strings.Contains(pe.Message, "unknown directive") || !strings.Contains(pe.Message, "conf.example.com.conf:6") || strings.Contains(pe.Message, "<") {
		t.Fatalf("message = %q", pe.Message)
	}
	if err := c.SetNginxConf(ctx, s, cur.Content, ""); err != nil {
		t.Fatal(err)
	}
	if err := c.TestNginx(ctx); err != nil {
		t.Fatal(err)
	}
	f.Lock()
	running := f.NginxRunning
	f.Unlock()
	if !running {
		t.Fatal("the panel starts nginx after a test")
	}
	if form := lastForm(f, "system/ServiceAdmin"); form.Get("name") != "nginx" || form.Get("type") != "test" {
		t.Fatalf("ServiceAdmin form = %v", form)
	}
}

func TestConfPaths(t *testing.T) {
	for typ, want := range map[string]string{
		"PHP": "a.com.conf", "": "a.com.conf", "WP2": "a.com.conf", "proxy": "a.com.conf",
		"Node": "node_a.com.conf", "Java": "java_a.com.conf", "Go": "go_a.com.conf", "Python": "python_a.com.conf",
		"Other": "other_a.com.conf", "net": "net_a.com.conf", "html": "html_a.com.conf",
	} {
		if got := NginxConfPath(Site{Name: "a.com", ProjectType: typ}); got != "/www/server/panel/vhost/nginx/"+want {
			t.Errorf("%s: %s", typ, got)
		}
	}
}

func TestSiteLogs(t *testing.T) {
	f, c := setup(t)
	f.AddSite("log.example.com")
	s, _ := c.Site(ctx, "log.example.com")
	if out, err := c.SiteLog(ctx, s, 10); err != nil || out != "" {
		t.Fatalf("no log yet: %q %v", out, err)
	}
	f.WriteFile("/www/wwwlogs/log.example.com.log", `1.1.1.1 - - "GET / HTTP/1.1" 200
2.2.2.2 - - "GET /a?x=<b>&y='c' HTTP/1.1" 404
3.3.3.3 - - "POST /login HTTP/1.1" 302
`)
	out, err := c.SiteLog(ctx, s, 2)
	if err != nil || out != "2.2.2.2 - - \"GET /a?x=<b>&y='c' HTTP/1.1\" 404\n3.3.3.3 - - \"POST /login HTTP/1.1\" 302" {
		t.Fatalf("log = %q, %v", out, err)
	}
	if out, _ := c.SiteLog(ctx, s, 0); strings.Count(out, "\n") != 2 {
		t.Fatalf("whole log = %q", out)
	}

	f.WriteFile("/www/wwwlogs/log.example.com.error.log", "2026/09/28 [error] open() failed\n")
	if out, err := c.SiteErrorLog(ctx, s, 5); err != nil || out != "2026/09/28 [error] open() failed" {
		t.Fatalf("error log = %q, %v", out, err)
	}

	// aaPanel names the error log action get_site_err_log.
	aa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "get_site_errlog":
			_, _ = io.WriteString(w, `{"status": false, "msg": "Specific parameters are invalid!"}`)
			return
		case "get_site_err_log":
			q := r.URL.Query()
			q.Set("action", "get_site_errlog")
			r.URL.RawQuery = q.Encode()
		}
		f.ServeHTTP(w, r)
	}))
	defer aa.Close()
	c = New(btpaneltest.Transport(aa.URL), 8888, f.Key, "http")
	if out, err := c.SiteErrorLog(ctx, s, 5); err != nil || out != "2026/09/28 [error] open() failed" {
		t.Fatalf("aaPanel error log = %q, %v", out, err)
	}
}

func TestBackups(t *testing.T) {
	f, c := setup(t)
	f.AddSite("bak.example.com")
	s, _ := c.Site(ctx, "bak.example.com")

	b, err := c.BackupSite(ctx, s)
	if err != nil || b.Type != 0 || b.OwnerID != s.ID || b.Size <= 1024 || !strings.HasPrefix(b.File, "/www/backup/site/bak.example.com/bak.example.com_") ||
		!strings.HasSuffix(b.Name, ".tar.gz") || b.Running || b.Missing || b.Remark != "手动备份" {
		t.Fatalf("backup = %+v, %v", b, err)
	}
	b2, _ := c.BackupSite(ctx, s)
	list, err := c.SiteBackups(ctx, s)
	if err != nil || len(list) != 2 || list[0].ID != b2.ID || b2.ID <= b.ID {
		t.Fatalf("backups = %+v, %v", list, err)
	}
	if s2, _ := c.Site(ctx, s.Name); s2.BackupCount != 2 {
		t.Fatalf("backup count = %d", s2.BackupCount)
	}
	if err := c.DeleteBackup(ctx, b); err != nil {
		t.Fatal(err)
	}
	if list, _ := c.SiteBackups(ctx, s); len(list) != 1 || list[0].ID != b2.ID {
		t.Fatalf("after delete = %+v", list)
	}
	if form := lastForm(f, "data/getData"); form.Get("table") != "backup" || form.Get("type") != "0" || form.Get("search") != fmt.Sprint(s.ID) {
		t.Fatalf("backup list form = %v", form)
	}

	db := f.AddDatabase("shop")
	d := Database{ID: db.ID, Name: db.Name}
	db1, err := c.BackupDatabase(ctx, d)
	if err != nil || db1.Type != 1 || db1.OwnerID != d.ID || !strings.HasSuffix(db1.File, ".sql.zip") {
		t.Fatalf("db backup = %+v, %v", db1, err)
	}
	if list, _ := c.DatabaseBackups(ctx, d); len(list) != 1 || list[0].ID != db1.ID {
		t.Fatalf("db backups = %+v", list)
	}
	if err := c.RestoreDatabase(ctx, d, db1); err != nil {
		t.Fatal(err)
	}
	f.Lock()
	restored := db.Restored
	f.Unlock()
	if restored != db1.File {
		t.Fatalf("restored %q", restored)
	}
	if err := c.RestoreDatabase(ctx, d, Backup{File: "/tmp/nothing.sql"}); err == nil || !strings.Contains(err.Error(), "导入路径不存在") {
		t.Fatalf("restore missing file: %v", err)
	}
	if err := c.DeleteBackup(ctx, db1); err != nil {
		t.Fatal(err)
	}
	if lastForm(f, "database/DelBackup").Get("id") != fmt.Sprint(db1.ID) {
		t.Fatal("database backup deleted through the wrong module")
	}
	if err := c.DeleteBackup(ctx, db1); err == nil || !strings.Contains(err.Error(), "备份已删除") {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestDatabases(t *testing.T) {
	f, c := setup(t)
	f.AddSite("db.example.com")
	s, _ := c.Site(ctx, "db.example.com")

	if _, err := c.CreateDatabase(ctx, NewDatabase{Name: "shop"}); err == nil || !strings.Contains(err.Error(), "密码") {
		t.Fatalf("no password: %v", err)
	}
	d, err := c.CreateDatabase(ctx, NewDatabase{Name: "shop", Password: "S3cret!pw", Remark: "商城", SiteID: s.ID})
	if err != nil || d.Name != "shop" || d.User != "shop" || d.Access != "127.0.0.1" || d.Remark != "商城" || d.SiteID != s.ID || d.ID == 0 {
		t.Fatalf("created %+v, %v", d, err)
	}
	form := lastForm(f, "database/AddDatabase")
	if form.Get("codeing") != "utf8mb4" || form.Get("address") != "127.0.0.1" || form.Get("dataAccess") != "127.0.0.1" ||
		form.Get("db_user") != "shop" || form.Get("password") != "S3cret!pw" || form.Get("pid") != fmt.Sprint(s.ID) {
		t.Fatalf("AddDatabase form = %v", form)
	}
	if _, err := c.CreateDatabase(ctx, NewDatabase{Name: "shop", User: "shop2", Password: "x"}); err == nil || !strings.Contains(err.Error(), "指定数据库已在MySQL中存在") {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := c.CreateDatabase(ctx, NewDatabase{Name: "bad name", Password: "x"}); err == nil || !strings.Contains(err.Error(), "数据库名称不能带有特殊符号") {
		t.Fatalf("bad name: %v", err)
	}
	f.AddDatabase("blog")
	list, err := c.Databases(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "blog" || list[1].Name != "shop" {
		t.Fatalf("databases = %+v, %v", list, err)
	}
	if strings.Contains(fmt.Sprintf("%+v", list), "secret-blog") {
		t.Fatal("the password came through")
	}
	if err := c.DeleteDatabase(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteDatabase(ctx, d); err == nil || !strings.Contains(err.Error(), "数据库[shop]不存在") {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestTraceNeverHasCredentials(t *testing.T) {
	f, c := setup(t)
	var traced strings.Builder
	c.Trace = func(method, path string, body []byte) { traced.WriteString(string(body) + "\n") }
	f.AddSite("t.example.com")
	s, _ := c.Site(ctx, "t.example.com")
	_, _ = c.CreateDatabase(ctx, NewDatabase{Name: "tdb", Password: "hunter22"})
	cert, key := btpaneltest.NewCert("t.example.com")
	_ = c.SetSSL(ctx, s, key, cert)
	for _, secret := range []string{"request_token", "request_time", f.Key, "hunter22", "PRIVATE"} {
		if strings.Contains(traced.String(), secret) {
			t.Fatalf("trace has %q:\n%s", secret, traced.String())
		}
	}
}

func TestPlain(t *testing.T) {
	in := `ERROR: <br><a style="color:red;">nginx: [emerg] &quot;x&quot;<br>test failed</a>`
	if got := plain(in); got != "ERROR:\nnginx: [emerg] \"x\"\ntest failed" {
		t.Fatalf("plain = %q", got)
	}
	if got := msgText([]byte(`["验证失败 ", {"detail": "x"}]`)); got != "验证失败 " {
		t.Fatalf("msgText = %q", got)
	}
	if n, ok := pageTotal("<div><span class='Pcount'>Total 57</span></div>"); !ok || n != 57 {
		t.Fatalf("aaPanel pager: %d %v", n, ok)
	}
}
