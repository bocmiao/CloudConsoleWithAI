package actions

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// COS bucket settings. Each change remembers the setting it replaced, so
// it can be undone; files in buckets are not changed here.

// LifeRule is a lifecycle rule as the storage page and the AI write it.
type LifeRule struct {
	ID             string `json:"id"`
	Prefix         string `json:"prefix"`
	Enabled        bool   `json:"enabled"`
	IADays         int    `json:"iaDays,omitempty"`         // then to 低频存储
	ArchiveDays    int    `json:"archiveDays,omitempty"`    // then to 归档存储
	DeepDays       int    `json:"deepDays,omitempty"`       // then to 深度归档存储
	ExpireDays     int    `json:"expireDays,omitempty"`     // then deleted
	NoncurrentDays int    `json:"noncurrentDays,omitempty"` // old versions deleted after
	AbortDays      int    `json:"abortDays,omitempty"`      // unfinished uploads cleaned after
}

var (
	bucketRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?-[0-9]{6,12}$`)
	refererRe = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9*]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)
	originRe  = regexp.MustCompile(`^(\*|https?://(\*\.)?[A-Za-z0-9.-]+(:[0-9]{1,5})?)$`)
	ruleIDRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

// splitLines splits a list written one per line or with commas.
func splitLines(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' || r == '，' || r == ' ' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ParseLifeRules reads and checks lifecycle rules in the page's form.
func ParseLifeRules(s string) ([]LifeRule, error) {
	var rules []LifeRule
	if err := json.Unmarshal([]byte(s), &rules); err != nil {
		return nil, fmt.Errorf("生命周期规则的格式不对：%v", err)
	}
	if len(rules) > 100 {
		return nil, fmt.Errorf("生命周期规则最多 100 条")
	}
	ids := map[string]bool{}
	for i := range rules {
		r := &rules[i]
		if r.ID == "" {
			r.ID = "rule-" + strconv.Itoa(i+1)
		}
		if !ruleIDRe.MatchString(r.ID) {
			return nil, fmt.Errorf("规则名称 %q 只能用字母、数字和 _.-", r.ID)
		}
		if ids[r.ID] {
			return nil, fmt.Errorf("规则名称 %q 重复了", r.ID)
		}
		ids[r.ID] = true
		if len(r.Prefix) > 1024 || strings.ContainsAny(r.Prefix, "\x00\n\r") {
			return nil, fmt.Errorf("规则 %s 的前缀不对", r.ID)
		}
		days := []int{r.IADays, r.ArchiveDays, r.DeepDays, r.ExpireDays, r.NoncurrentDays, r.AbortDays}
		any := false
		for _, d := range days {
			if d < 0 || d > 36500 {
				return nil, fmt.Errorf("规则 %s 的天数要在 1 到 36500 之间", r.ID)
			}
			any = any || d > 0
		}
		if !any {
			return nil, fmt.Errorf("规则 %s 没有设置任何动作", r.ID)
		}
		last := 0
		for _, d := range []int{r.IADays, r.ArchiveDays, r.DeepDays} {
			if d > 0 {
				if d <= last {
					return nil, fmt.Errorf("规则 %s：转存储类型的天数要依次变大（低频 < 归档 < 深度归档）", r.ID)
				}
				last = d
			}
		}
		if r.ExpireDays > 0 && r.ExpireDays <= last {
			return nil, fmt.Errorf("规则 %s：删除的天数要比转存储类型的天数大", r.ID)
		}
	}
	return rules, nil
}

// LifecycleXML writes the rules as COS wants them, after the rules of
// the current configuration named in keep, which stay exactly as they are.
func LifecycleXML(rules []LifeRule, current string, keep []string) (string, error) {
	var b strings.Builder
	n := 0
	if len(keep) > 0 {
		var cur struct {
			Rules []struct {
				ID    string `xml:"ID"`
				Inner string `xml:",innerxml"`
			} `xml:"Rule"`
		}
		if err := xml.Unmarshal([]byte(current), &cur); err != nil && strings.TrimSpace(current) != "" {
			return "", fmt.Errorf("读不懂现在的生命周期规则：%v", err)
		}
		for _, id := range keep {
			found := false
			for _, r := range cur.Rules {
				if r.ID == id {
					b.WriteString("<Rule>" + r.Inner + "</Rule>")
					found, n = true, n+1
				}
			}
			if !found {
				return "", fmt.Errorf("现在的生命周期规则里没有 %s（可能在别处改过了）", id)
			}
		}
	}
	esc := func(s string) string {
		var e strings.Builder
		_ = xml.EscapeText(&e, []byte(s))
		return e.String()
	}
	for _, r := range rules {
		status := "Disabled"
		if r.Enabled {
			status = "Enabled"
		}
		b.WriteString("<Rule><ID>" + esc(r.ID) + "</ID><Filter>")
		if r.Prefix != "" {
			b.WriteString("<Prefix>" + esc(r.Prefix) + "</Prefix>")
		}
		b.WriteString("</Filter><Status>" + status + "</Status>")
		for _, t := range []struct {
			days  int
			class string
		}{{r.IADays, "STANDARD_IA"}, {r.ArchiveDays, "ARCHIVE"}, {r.DeepDays, "DEEP_ARCHIVE"}} {
			if t.days > 0 {
				fmt.Fprintf(&b, "<Transition><Days>%d</Days><StorageClass>%s</StorageClass></Transition>", t.days, t.class)
			}
		}
		if r.NoncurrentDays > 0 {
			fmt.Fprintf(&b, "<NoncurrentVersionExpiration><NoncurrentDays>%d</NoncurrentDays></NoncurrentVersionExpiration>", r.NoncurrentDays)
		}
		if r.ExpireDays > 0 {
			fmt.Fprintf(&b, "<Expiration><Days>%d</Days></Expiration>", r.ExpireDays)
		}
		if r.AbortDays > 0 {
			fmt.Fprintf(&b, "<AbortIncompleteMultipartUpload><DaysAfterInitiation>%d</DaysAfterInitiation></AbortIncompleteMultipartUpload>", r.AbortDays)
		}
		b.WriteString("</Rule>")
		n++
	}
	if n == 0 {
		return "", nil
	}
	return "<LifecycleConfiguration>" + b.String() + "</LifecycleConfiguration>", nil
}

// ParseCORSRules reads and checks cross-origin rules.
func ParseCORSRules(s string) ([]tencent.CORSRule, error) {
	var rules []tencent.CORSRule
	if err := json.Unmarshal([]byte(s), &rules); err != nil {
		return nil, fmt.Errorf("跨域规则的格式不对：%v", err)
	}
	if len(rules) > 100 {
		return nil, fmt.Errorf("跨域规则最多 100 条")
	}
	methods := map[string]bool{"GET": true, "PUT": true, "POST": true, "DELETE": true, "HEAD": true}
	for i := range rules {
		r := &rules[i]
		if len(r.Origins) == 0 || len(r.Methods) == 0 {
			return nil, fmt.Errorf("第 %d 条跨域规则要写来源和允许的方法", i+1)
		}
		for _, o := range r.Origins {
			if !originRe.MatchString(o) {
				return nil, fmt.Errorf("来源 %q 不对：写成 https://example.com、https://*.example.com 或 *", o)
			}
		}
		for j, m := range r.Methods {
			r.Methods[j] = strings.ToUpper(m)
			if !methods[r.Methods[j]] {
				return nil, fmt.Errorf("方法 %q 不对：只能是 GET、PUT、POST、DELETE、HEAD", m)
			}
		}
		for _, h := range append(append([]string{}, r.Headers...), r.Expose...) {
			if h == "" || len(h) > 128 || strings.ContainsAny(h, " \t\r\n:") {
				return nil, fmt.Errorf("头部 %q 不对", h)
			}
		}
		if r.MaxAge < 0 || r.MaxAge > 31536000 {
			return nil, fmt.Errorf("缓存时间要在 0 到 31536000 秒之间")
		}
	}
	return rules, nil
}

func checkReferer(v map[string]string) error {
	if v["status"] == "on" && len(splitLines(v["domains"])) == 0 {
		return fmt.Errorf("开启防盗链要填至少一个域名")
	}
	for _, d := range splitLines(v["domains"]) {
		if !refererRe.MatchString(strings.ToLower(d)) {
			return fmt.Errorf("域名 %q 不对：写成 example.com、*.example.com 或 example.com:8080", d)
		}
	}
	return nil
}

func init() {
	bucket := Param{Name: "bucket", Kind: "bucket", Required: true, Desc: "存储桶名称，带 APPID，例如 blog-1250000000（tencent_cos 返回的 name）"}
	region := Param{Name: "region", Kind: "region", Required: true, Desc: "存储桶所在地域，例如 ap-guangzhou"}
	cloud := func(op, downtime, undo string) map[string]Impl {
		return map[string]Impl{"*": {Via: "腾讯云接口", Cloud: op, Downtime: downtime, Undo: undo}}
	}
	register(&Capability{
		Name: "cos.bucket.create", Title: "新建存储桶", Risk: core.R1, Reversible: true,
		Params: []Param{bucket, region,
			{Name: "acl", Kind: "enum", Enum: []string{"private", "public-read"}, Default: "private", Desc: "private 私有读写（推荐）；public-read 公有读私有写"}},
		Impls: cloud("cos_create", "不影响现有的存储桶", "删除这个存储桶（里面没有文件时）"),
	})
	register(&Capability{
		Name: "cos.bucket.delete", Title: "删除存储桶", Risk: core.R3,
		NoUndo: "存储桶删除后找不回，名字也可能被别人占用",
		Params: []Param{bucket, region},
		Impls:  map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "cos_delete", Downtime: "只能删除空的存储桶"}},
	})
	register(&Capability{
		Name: "cos.acl.set", Title: "设置存储桶访问权限", Risk: core.R2, Reversible: true,
		Params: []Param{bucket, region,
			{Name: "acl", Kind: "enum", Enum: []string{"private", "public-read", "public-read-write"}, Required: true,
				Desc: "private 私有读写；public-read 公有读私有写（任何人能下载和列出文件）；public-read-write 公有读写（任何人能上传和删除，极不安全）"}},
		Impls: cloud("cos_acl", "立即生效", "恢复原来的访问权限（包括给其他账号的授权）"),
	})
	register(&Capability{
		Name: "cos.referer.set", Title: "设置防盗链", Risk: core.R2, Reversible: true,
		Params: []Param{bucket, region,
			{Name: "status", Kind: "enum", Enum: []string{"on", "off"}, Required: true, Desc: "on 开启，off 关闭"},
			{Name: "type", Kind: "enum", Enum: []string{"white", "black"}, Default: "white", Desc: "white 白名单（只有这些网站能引用）；black 黑名单（这些网站不能引用）"},
			{Name: "domains", Kind: "lines", Desc: "域名，一行一个或用逗号分隔，例如 example.com、*.example.com"},
			{Name: "allow_empty", Kind: "enum", Enum: []string{"yes", "no"}, Default: "yes", Desc: "yes 允许没有 Referer 的访问（直接打开链接、App 里访问）；no 拒绝"}},
		Impls: cloud("cos_referer", "立即生效；白名单以外的网站引用这个桶的文件会被拒绝", "恢复原来的防盗链设置"),
		Check: checkReferer,
	})
	register(&Capability{
		Name: "cos.cors.set", Title: "设置跨域规则", Risk: core.R2, Reversible: true,
		Params: []Param{bucket, region,
			{Name: "rules", Kind: "json", Required: true,
				Desc: `规则列表（JSON），[] 表示删除全部。每条：{"origins":["https://example.com"],"methods":["GET","PUT"],"headers":["*"],"expose":["ETag"],"maxAge":600}`}},
		Impls: cloud("cos_cors", "立即生效", "恢复原来的跨域规则"),
		Check: func(v map[string]string) error { _, err := ParseCORSRules(v["rules"]); return err },
	})
	register(&Capability{
		Name: "cos.lifecycle.set", Title: "设置生命周期规则", Risk: core.R2, Reversible: true,
		Params: []Param{bucket, region,
			{Name: "rules", Kind: "json", Required: true,
				Desc: `规则列表（JSON），[] 表示删除全部。每条：{"id":"backup-30d","prefix":"backup/","enabled":true,"iaDays":30,"archiveDays":90,"deepDays":180,"expireDays":365,"noncurrentDays":30,"abortDays":7}，` +
					`prefix 为空表示整个桶；iaDays/archiveDays/deepDays 是多少天后转低频/归档/深度归档，expireDays 是多少天后删除，noncurrentDays 是历史版本多少天后删除，abortDays 是多少天后清理没传完的碎片`},
			{Name: "keep", Kind: "text", Desc: "现有规则里要原样保留的规则名称，逗号分隔（这里不能编辑的规则）"}},
		Impls: cloud("cos_lifecycle", "规则每天执行一次；到期删除的文件找不回", "恢复原来的生命周期规则（已经被删除的文件恢复不了）"),
		Check: func(v map[string]string) error { _, err := ParseLifeRules(v["rules"]); return err },
	})
	register(&Capability{
		Name: "cos.versioning.set", Title: "设置版本控制", Risk: core.R1, Reversible: true,
		Params: []Param{bucket, region,
			{Name: "status", Kind: "enum", Enum: []string{"on", "suspend"}, Required: true, Desc: "on 开启（覆盖和删除的文件保留历史版本，历史版本也占存储费用）；suspend 暂停（开启过就不能关闭，只能暂停）"}},
		Impls: cloud("cos_versioning", "立即生效，只影响之后的上传和删除", "恢复原来的状态（开启过的桶只能暂停，不能回到从未开启）"),
	})
	register(&Capability{
		Name: "cos.encryption.set", Title: "设置服务端加密", Risk: core.R1, Reversible: true,
		Params: []Param{bucket, region,
			{Name: "status", Kind: "enum", Enum: []string{"on", "off"}, Required: true, Desc: "on 用 COS 管理的密钥（SSE-COS，AES256）加密之后上传的文件；off 关闭"}},
		Impls: cloud("cos_encryption", "只影响之后上传的文件，下载时自动解密", "恢复原来的加密设置"),
	})
	register(&Capability{
		Name: "cos.website.set", Title: "设置静态网站", Risk: core.R2, Reversible: true,
		Params: []Param{bucket, region,
			{Name: "status", Kind: "enum", Enum: []string{"on", "off"}, Required: true, Desc: "on 开启，off 关闭"},
			{Name: "index", Kind: "text", Default: "index.html", Desc: "首页文件"},
			{Name: "error", Kind: "text", Desc: "找不到页面时显示的文件，例如 404.html"},
			{Name: "https", Kind: "enum", Enum: []string{"yes", "no"}, Default: "no", Desc: "yes 把 HTTP 访问都跳转到 HTTPS"}},
		Impls: cloud("cos_website", "立即生效（存储桶要公有读，静态网站地址才能访问）", "恢复原来的静态网站设置"),
	})
	register(&Capability{
		Name: "cos.policy.set", Title: "设置存储桶策略", Risk: core.R2, Reversible: true,
		Params: []Param{bucket, region,
			{Name: "policy", Kind: "json", Desc: `存储桶策略（JSON，COS 的格式），空或 {} 表示删除策略`}},
		Impls: cloud("cos_policy", "立即生效", "恢复原来的存储桶策略"),
	})
}

func cosWhere(v map[string]string) (string, string) { return v["bucket"], v["region"] }

func applyCOS(ctx context.Context, env *Env, op string, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	b, r := cosWhere(v)
	done := func(format string, args ...any) Outcome {
		report(format, args...)
		out.Status = StatusDone
		return *out
	}
	unchanged := func(format string, args ...any) Outcome {
		out.Undo = map[string]string{}
		return done(format, args...)
	}
	fail := func(what string, err error) Outcome { return refuse(out, "%s失败：%v", what, err) }
	out.Undo["bucket"], out.Undo["region"] = b, r
	switch op {
	case "cos_create":
		report("正在新建存储桶 %s（%s，%s）", b, r, map[string]string{"private": "私有读写", "public-read": "公有读私有写"}[v["acl"]])
		if err := c.CreateBucket(ctx, b, r, v["acl"]); err != nil {
			return fail("新建", err)
		}
		return done("完成：已新建 %s", b)
	case "cos_delete":
		l, err := c.ListObjects(ctx, b, r, "", "", "", 1)
		if err != nil {
			return fail("读取存储桶", err)
		}
		if len(l.Objects) > 0 {
			return refuse(out, "%s 里还有文件，没有删除。要删除存储桶，先把里面的文件删光", b)
		}
		report("正在删除存储桶 %s", b)
		if err := c.DeleteBucket(ctx, b, r); err != nil {
			return fail("删除", err)
		}
		out.Undo = map[string]string{}
		return done("完成：已删除 %s", b)
	case "cos_acl":
		cur, err := c.BucketACL(ctx, b, r)
		if err != nil {
			return fail("读取访问权限", err)
		}
		if cur.Canned == v["acl"] {
			return unchanged("%s 的访问权限已经是 %s，不需要修改", b, v["acl"])
		}
		prev, _ := json.Marshal(cur)
		want := tencent.ACL{Canned: v["acl"], Grants: cur.Grants} // grants to other accounts stay
		report("正在把 %s 的访问权限从 %s 改为 %s", b, cur.Canned, want.Canned)
		if err := c.SetBucketACL(ctx, b, r, want); err != nil {
			return fail("修改", err)
		}
		out.Undo["acl"] = string(prev)
		return done("完成，立即生效")
	case "cos_referer":
		cur, err := c.BucketReferer(ctx, b, r)
		if err != nil {
			return fail("读取防盗链", err)
		}
		want := tencent.Referer{Status: "Disabled", Type: "White-List", Domains: splitLines(strings.ToLower(v["domains"])), EmptyRefer: "Allow"}
		if v["status"] == "on" {
			want.Status = "Enabled"
		}
		if v["type"] == "black" {
			want.Type = "Black-List"
		}
		if v["allow_empty"] == "no" {
			want.EmptyRefer = "Deny"
		}
		if want.Status == "Disabled" && len(want.Domains) == 0 {
			want.Domains = cur.Domains // switching off keeps the list for next time
		}
		prev, _ := json.Marshal(cur)
		report("正在%s %s 的防盗链", map[string]string{"Enabled": "设置", "Disabled": "关闭"}[want.Status], b)
		if err := c.SetBucketReferer(ctx, b, r, want); err != nil {
			return fail("设置防盗链", err)
		}
		out.Undo["referer"] = string(prev)
		return done("完成，几分钟内生效")
	case "cos_cors":
		rules, _ := ParseCORSRules(v["rules"])
		cur, err := c.BucketCORS(ctx, b, r)
		if err != nil {
			return fail("读取跨域规则", err)
		}
		prev, _ := json.Marshal(cur)
		report("正在把 %s 的跨域规则改为 %d 条", b, len(rules))
		if err := c.SetBucketCORS(ctx, b, r, rules); err != nil {
			return fail("设置跨域规则", err)
		}
		out.Undo["cors"] = string(prev)
		return done("完成，立即生效")
	case "cos_lifecycle":
		rules, _ := ParseLifeRules(v["rules"])
		raw, _, err := c.BucketLifecycle(ctx, b, r)
		if err != nil {
			return fail("读取生命周期规则", err)
		}
		var keep []string
		for _, id := range strings.Split(v["keep"], ",") {
			if id = strings.TrimSpace(id); id != "" {
				keep = append(keep, id)
			}
		}
		next, err := LifecycleXML(rules, raw, keep)
		if err != nil {
			return refuse(out, "%v", err)
		}
		if next == raw {
			return unchanged("%s 的生命周期规则没有变化", b)
		}
		report("正在把 %s 的生命周期规则改为 %d 条", b, len(rules)+len(keep))
		if err := c.SetBucketLifecycleXML(ctx, b, r, next); err != nil {
			return fail("设置生命周期规则", err)
		}
		out.Undo["lifecycle"] = raw
		out.Undo["had_lifecycle"] = strconv.FormatBool(raw != "")
		return done("完成：规则每天执行一次")
	case "cos_versioning":
		cur, err := c.BucketVersioning(ctx, b, r)
		if err != nil {
			return fail("读取版本控制", err)
		}
		want := map[string]string{"on": "Enabled", "suspend": "Suspended"}[v["status"]]
		if cur == want || (want == "Suspended" && cur == "") {
			return unchanged("%s 的版本控制已经是%s，不需要修改", b, map[string]string{"Enabled": "开启", "Suspended": "暂停", "": "未开启"}[cur])
		}
		report("正在%s %s 的版本控制", map[string]string{"Enabled": "开启", "Suspended": "暂停"}[want], b)
		if err := c.SetBucketVersioning(ctx, b, r, want); err != nil {
			return fail("设置版本控制", err)
		}
		out.Undo["versioning"] = cur
		return done("完成，之后的上传和删除按新设置处理")
	case "cos_encryption":
		cur, err := c.BucketEncryption(ctx, b, r)
		if err != nil {
			return fail("读取加密设置", err)
		}
		want := ""
		if v["status"] == "on" {
			want = "AES256"
		}
		if cur == want {
			return unchanged("%s 的服务端加密已经是%s，不需要修改", b, map[bool]string{true: "开启", false: "关闭"}[want != ""])
		}
		report("正在%s %s 的服务端加密", map[bool]string{true: "开启", false: "关闭"}[want != ""], b)
		if err := c.SetBucketEncryption(ctx, b, r, want); err != nil {
			return fail("设置加密", err)
		}
		out.Undo["encryption"] = cur
		return done("完成：只影响之后上传的文件")
	case "cos_website":
		cur, err := c.BucketWebsite(ctx, b, r)
		if err != nil {
			return fail("读取静态网站设置", err)
		}
		want := tencent.Website{Enabled: v["status"] == "on", Index: v["index"], Error: v["error"], HTTPS: v["https"] == "yes"}
		if want.Enabled && want.Index == "" {
			want.Index = "index.html"
		}
		prev, _ := json.Marshal(cur)
		report("正在%s %s 的静态网站", map[bool]string{true: "设置", false: "关闭"}[want.Enabled], b)
		if err := c.SetBucketWebsite(ctx, b, r, want); err != nil {
			return fail("设置静态网站", err)
		}
		out.Undo["website"] = string(prev)
		if want.Enabled {
			return done("完成：静态网站地址是 https://%s.cos-website.%s.myqcloud.com", b, r)
		}
		return done("完成")
	case "cos_policy":
		cur, err := c.BucketPolicy(ctx, b, r)
		if err != nil {
			return fail("读取存储桶策略", err)
		}
		want := strings.TrimSpace(v["policy"])
		if want == "{}" {
			want = ""
		}
		if want == strings.TrimSpace(cur) {
			return unchanged("%s 的存储桶策略没有变化", b)
		}
		report("正在%s %s 的存储桶策略", map[bool]string{true: "修改", false: "删除"}[want != ""], b)
		if err := c.SetBucketPolicy(ctx, b, r, want); err != nil {
			return fail("设置存储桶策略", err)
		}
		out.Undo["policy"] = cur
		return done("完成，立即生效")
	}
	return refuse(out, "未知的操作 %s", op)
}

func undoCOS(ctx context.Context, env *Env, op string, undo map[string]string) error {
	c := env.Cloud
	b, r := undo["bucket"], undo["region"]
	switch op {
	case "cos_create":
		l, err := c.ListObjects(ctx, b, r, "", "", "", 1)
		if err != nil {
			return err
		}
		if len(l.Objects) > 0 {
			return fmt.Errorf("%s 里已经有文件了，没有删除它", b)
		}
		return c.DeleteBucket(ctx, b, r)
	case "cos_acl":
		var a tencent.ACL
		if err := json.Unmarshal([]byte(undo["acl"]), &a); err != nil {
			return err
		}
		return c.SetBucketACL(ctx, b, r, a)
	case "cos_referer":
		var ref tencent.Referer
		if err := json.Unmarshal([]byte(undo["referer"]), &ref); err != nil {
			return err
		}
		return c.SetBucketReferer(ctx, b, r, ref)
	case "cos_cors":
		var rules []tencent.CORSRule
		if err := json.Unmarshal([]byte(undo["cors"]), &rules); err != nil {
			return err
		}
		return c.SetBucketCORS(ctx, b, r, rules)
	case "cos_lifecycle":
		return c.SetBucketLifecycleXML(ctx, b, r, undo["lifecycle"])
	case "cos_versioning":
		prev := undo["versioning"]
		if prev == "" {
			prev = "Suspended" // once turned on, versioning can only be paused
		}
		return c.SetBucketVersioning(ctx, b, r, prev)
	case "cos_encryption":
		return c.SetBucketEncryption(ctx, b, r, undo["encryption"])
	case "cos_website":
		var w tencent.Website
		if err := json.Unmarshal([]byte(undo["website"]), &w); err != nil {
			return err
		}
		return c.SetBucketWebsite(ctx, b, r, w)
	case "cos_policy":
		return c.SetBucketPolicy(ctx, b, r, undo["policy"])
	}
	return fmt.Errorf("未知的操作 %s", op)
}
