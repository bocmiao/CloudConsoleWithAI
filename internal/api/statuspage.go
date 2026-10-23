package api

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
)

// statusPageHTML is the public, read-only status page. It carries its own
// styles so it works without the console's assets or a login.
const statusPageHTML = `<!doctype html>
<html lang="zh-CN"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<meta http-equiv="refresh" content="60">
<title>服务状态</title>
<style>
:root{
  --bg:#eef1f7;--card:rgba(255,255,255,.72);--line:rgba(60,60,67,.14);
  --text:#1d1d1f;--text2:#6e6e73;--good:#34c759;--warn:#ff9500;--crit:#ff3b30;--accent:#007aff;
}
@media (prefers-color-scheme: dark){
  :root{--bg:#0d0e12;--card:rgba(28,29,34,.72);--line:rgba(255,255,255,.12);--text:#f2f2f7;--text2:#98989f;}
}
*{box-sizing:border-box}
body{margin:0;padding:40px 18px 64px;background:
  radial-gradient(1200px 600px at 10% -10%, rgba(0,122,255,.18), transparent 60%),
  radial-gradient(900px 500px at 100% 0%, rgba(52,199,89,.14), transparent 55%), var(--bg);
  color:var(--text);font:15px/1.6 -apple-system,BlinkMacSystemFont,"SF Pro SC","PingFang SC","Microsoft YaHei",sans-serif;
  -webkit-font-smoothing:antialiased;min-height:100vh}
.wrap{max-width:920px;margin:0 auto}
header{display:flex;align-items:center;gap:14px;margin-bottom:22px}
h1{font-size:24px;letter-spacing:-.02em;margin:0}
.sub{color:var(--text2);font-size:13px}
.pill{display:inline-flex;align-items:center;gap:8px;padding:8px 14px;border-radius:999px;
  background:var(--card);border:1px solid var(--line);backdrop-filter:blur(22px) saturate(180%);
  -webkit-backdrop-filter:blur(22px) saturate(180%);box-shadow:0 8px 24px rgba(0,0,0,.06);font-size:13px}
.dot{width:9px;height:9px;border-radius:50%;background:var(--good);box-shadow:0 0 0 4px rgba(52,199,89,.16)}
.dot.bad{background:var(--crit);box-shadow:0 0 0 4px rgba(255,59,48,.16)}
.card{background:var(--card);border:1px solid var(--line);border-radius:18px;padding:6px 0;margin:0 0 18px;
  backdrop-filter:blur(24px) saturate(180%);-webkit-backdrop-filter:blur(24px) saturate(180%);
  box-shadow:0 10px 30px rgba(0,0,0,.07), inset 0 1px 0 rgba(255,255,255,.55)}
.card h2{font-size:13px;font-weight:600;color:var(--text2);margin:16px 20px 8px;letter-spacing:.02em}
.row{display:flex;align-items:center;gap:12px;padding:12px 20px;border-top:1px solid var(--line)}
.row:first-of-type{border-top:0}
.name{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.state{font-size:13px;color:var(--text2);white-space:nowrap}
.state.up{color:var(--good)}
.state.down{color:var(--crit)}
.metrics{display:flex;gap:14px;font-size:12.5px;color:var(--text2);font-variant-numeric:tabular-nums}
.empty{padding:16px 20px;color:var(--text2);font-size:13px}
footer{color:var(--text2);font-size:12.5px;text-align:center;margin-top:8px}
a{color:var(--accent);text-decoration:none}
@media (max-width:560px){.metrics{display:none} h1{font-size:20px}}
</style></head><body><div class="wrap">
<header>
  <div>
    <h1>服务状态</h1>
    <div class="sub">每 60 秒自动刷新 · 更新于 {{.Updated}}</div>
  </div>
</header>

<div class="pill"><span class="dot{{if not .AllUp}} bad{{end}}"></span>
{{if .AllUp}}所有服务运行正常{{else}}{{.Down}} 项异常{{end}}</div>

<h2 style="margin:22px 4px 10px;font-size:13px;color:var(--text2)">网站</h2>
<div class="card">
  {{if .Sites}}{{range .Sites}}
  <div class="row">
    <span class="name">{{.Name}}</span>
    {{if .UptimeStr}}<span class="metrics">{{.UptimeStr}} · {{.AvgMS}}ms</span>{{end}}
    <span class="state {{if .Up}}up{{else}}down{{end}}">{{if .Up}}正常{{else}}异常{{end}}</span>
  </div>
  {{end}}{{else}}<div class="empty">还没有正在监控的网站</div>{{end}}
</div>

{{if .Servers}}
<h2 style="margin:22px 4px 10px;font-size:13px;color:var(--text2)">服务器</h2>
<div class="card">
  {{range .Servers}}
  <div class="row">
    <span class="name">{{.Name}}</span>
    <span class="metrics">CPU {{.CPU}}% · 内存 {{.Mem}}% · 磁盘 {{.Disk}}%</span>
    <span class="state {{if .Up}}up{{else}}down{{end}}">{{if .Up}}在线{{else}}离线{{end}}</span>
  </div>
  {{end}}
</div>
{{end}}

{{if .Incidents}}
<h2 style="margin:22px 4px 10px;font-size:13px;color:var(--text2)">最近 7 天</h2>
<div class="card">
  {{range .Incidents}}
  <div class="row">
    <span class="name">{{if .Name}}{{.Name}}{{else}}{{.Target}}{{end}}{{if .Reason}}<span class="state"> · {{.Reason}}</span>{{end}}</span>
    <span class="state">{{.Started}}{{if .Ended}} → {{.Ended}}{{else}} · 持续中{{end}}</span>
  </div>
  {{end}}
</div>
{{end}}

<footer>由 Miao Panel 提供 · 数据来自实时监控，仅供参考</footer>
</div></body></html>`

var statusPageTmpl = template.Must(template.New("status").Parse(statusPageHTML))

// getStatusPage tells the console what the public page is set to.
func (s *Server) getStatusPage(_ http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.StatusPageSettings(), nil
}

// saveStatusPage turns the public page on or off, optionally making a new
// link so an old one stops working.
func (s *Server) saveStatusPage(_ http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Enabled   bool `json:"enabled"`
		Regenerate bool `json:"regenerate"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.app.SaveStatusPageSettings(req.Enabled, req.Regenerate)
}

// publicStatusPage serves the read-only page. No login: the token in the URL
// is the secret, and only enabled pages answer.
func (s *Server) publicStatusPage(w http.ResponseWriter, r *http.Request) {
	v, ok := s.app.PublicStatus(r.PathValue("token"))
	if !ok {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("没有这个状态页"))
		return
	}
	data := struct {
		Updated   string
		AllUp     bool
		Down      int
		Sites     []struct{ Name string; Up bool; UptimeStr string; AvgMS int }
		Servers   []struct{ Name string; Up bool; CPU, Mem, Disk int }
		Incidents []struct{ Name, Target, Reason, Started, Ended string }
	}{Updated: shortTime(v.UpdatedAt), AllUp: v.AllUp, Down: v.Down}
	for _, x := range v.Sites {
		up := ""
		if x.Uptime != nil {
			up = fmt.Sprintf("%.2f%%", *x.Uptime)
		}
		data.Sites = append(data.Sites, struct{ Name string; Up bool; UptimeStr string; AvgMS int }{x.Name, x.Up, up, x.AvgMS})
	}
	for _, x := range v.Servers {
		data.Servers = append(data.Servers, struct{ Name string; Up bool; CPU, Mem, Disk int }{x.Name, x.Up, x.CPU, x.Mem, x.Disk})
	}
	for _, x := range v.Incidents {
		data.Incidents = append(data.Incidents, struct{ Name, Target, Reason, Started, Ended string }{x.Name, x.Target, x.Reason, shortTime(x.Started), shortTime(x.Ended)})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	var b strings.Builder
	if err := statusPageTmpl.Execute(&b, data); err != nil {
		http.Error(w, "页面生成失败", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write([]byte(b.String()))
}

// shortTime turns an RFC3339 stamp into something short enough for a row.
func shortTime(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("01-02 15:04")
}