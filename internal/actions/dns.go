package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// Editing DNSPod records one at a time, as the 解析 page and the AI do:
// add, change, delete, pause or resume. dns.record.set (cloud.go) is the
// higher-level "make this name point there".

// RecordTypes are the record types Miao Panel edits.
var RecordTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS", "CAA", "SRV"}

const recordValueDesc = "记录值：A 填 IPv4，AAAA 填 IPv6，CNAME、MX、NS 填域名，TXT 填文本，" +
	`CAA 例如 0 issue "letsencrypt.org"，SRV 例如 5 0 5060 sip.example.com`

func init() {
	domain := Param{Name: "domain", Kind: "host", Required: true, Desc: "DNSPod 里的主域名，例如 example.com"}
	recordID := Param{Name: "record_id", Kind: "int", Min: 1, Max: 1 << 53, Required: true, Desc: "记录编号（tencent_dns 返回的 id）"}
	fields := func(add bool) []Param {
		ps := []Param{
			{Name: "subdomain", Kind: "subdomain", Required: true, Desc: "主机记录，例如 www、mail；主域名本身填 @，泛解析填 *"},
			{Name: "type", Kind: "enum", Enum: RecordTypes, Required: true, Desc: "记录类型"},
			{Name: "value", Kind: "text", Required: true, Desc: recordValueDesc},
			{Name: "line", Kind: "text", Desc: "解析线路，例如 默认、电信、联通、境外"},
			{Name: "ttl", Kind: "int", Min: 1, Max: 604800, Desc: "TTL（秒）"},
			{Name: "mx", Kind: "int", Min: 1, Max: 65535, Desc: "MX 优先级，数字越小越优先（MX 记录用）"},
			{Name: "remark", Kind: "text", Desc: "备注"},
		}
		if add {
			ps[3].Default, ps[3].Desc = tencent.DefaultLine, ps[3].Desc+"，不填是默认"
			ps[4].Default, ps[4].Desc = "600", ps[4].Desc+"，不填是 600"
			ps[5].Desc += "，不填是 10"
		} else {
			ps[3].Desc += "，不填保持原样"
			ps[4].Desc += "，不填保持原样"
			ps[6].Desc += "，不填保持原样"
		}
		return ps
	}
	register(&Capability{
		Name: "dns.record.add", Title: "添加解析记录", Risk: core.R2, Reversible: true,
		Params: append([]Param{domain}, fields(true)...),
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "dns_record_add", Downtime: "按 TTL 几分钟内在各地生效",
			Undo: "删除这条记录"}},
		Check: checkRecordParams,
	})
	register(&Capability{
		Name: "dns.record.modify", Title: "修改解析记录", Risk: core.R2, Reversible: true,
		Params: append([]Param{domain, recordID}, fields(false)...),
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "dns_record_modify", Downtime: "按 TTL 几分钟内在各地生效",
			Undo: "把这条记录改回修改前的样子"}},
		Check: checkRecordParams,
	})
	register(&Capability{
		Name: "dns.record.delete", Title: "删除解析记录", Risk: core.R2, Reversible: true,
		Params: []Param{domain, recordID},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "dns_record_delete", Downtime: "按 TTL 几分钟内在各地失效",
			Undo: "把删除的记录加回来（记录编号会变）"}},
	})
	register(&Capability{
		Name: "dns.record.status", Title: "暂停或启用解析记录", Risk: core.R2, Reversible: true,
		Params: []Param{domain, recordID,
			{Name: "status", Kind: "enum", Enum: []string{"disable", "enable"}, Required: true, Desc: "disable 暂停（记录保留但不生效），enable 启用"}},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "dns_record_status", Downtime: "按 TTL 几分钟内在各地生效",
			Undo: "恢复原来的状态"}},
	})
}

var (
	caaRe  = regexp.MustCompile(`^[0-9]{1,3} (issue|issuewild|iodef) "[^"\\]{0,255}"$`)
	srvRe  = regexp.MustCompile(`^[0-9]{1,5} [0-9]{1,5} [0-9]{1,5} \S+$`)
	lineRe = regexp.MustCompile(`^[\p{Han}A-Za-z0-9_.()（）-]{1,32}$`)
)

// CheckRecord says whether value suits a record of type typ, in plain words.
func CheckRecord(typ, value string) error {
	if value == "" {
		return fmt.Errorf("记录值不能为空")
	}
	switch typ {
	case "MX", "NS":
		if !hostnameRe.MatchString(value) {
			return fmt.Errorf("%s 记录的值要是域名，例如 mail.example.com，%q 不是", typ, value)
		}
	case "TXT":
		if len(value) > 512 {
			return fmt.Errorf("TXT 记录最多 512 个字符")
		}
	case "CAA":
		if !caaRe.MatchString(value) {
			return fmt.Errorf(`CAA 记录的格式是 0 issue "letsencrypt.org"，%q 不对`, value)
		}
	case "SRV":
		f := strings.Fields(value)
		if !srvRe.MatchString(value) || !hostnameRe.MatchString(f[3]) && f[3] != "." {
			return fmt.Errorf("SRV 记录的格式是「优先级 权重 端口 目标域名」，例如 5 0 5060 sip.example.com，%q 不对", value)
		}
		for _, n := range f[:3] {
			if v, _ := strconv.Atoi(n); v > 65535 {
				return fmt.Errorf("SRV 记录里的 %s 超出范围", n)
			}
		}
	}
	return checkRecordValue(typ, value)
}

func checkRecordParams(v map[string]string) error {
	if err := CheckRecord(v["type"], v["value"]); err != nil {
		return err
	}
	if l := v["line"]; l != "" && !lineRe.MatchString(l) {
		return fmt.Errorf("线路 %q 不对", l)
	}
	return nil
}

func recordLabel(r tencent.Record) string {
	s := r.Type + " " + r.Value
	if r.Type == "MX" {
		s = fmt.Sprintf("MX %d %s", r.MX, r.Value)
	}
	if r.Line != "" && r.Line != tencent.DefaultLine {
		s += "（" + r.Line + "）"
	}
	return s
}

// findRecord looks a record up by ID.
func findRecord(ctx context.Context, c *tencent.Client, domain string, id uint64) (tencent.Record, bool, error) {
	all, err := c.Records(ctx, domain, "")
	if err != nil {
		return tencent.Record{}, false, fmt.Errorf("读取 %s 的解析记录失败：%w", domain, err)
	}
	for _, r := range all {
		if r.RecordID == id {
			return r, true, nil
		}
	}
	return tencent.Record{}, false, nil
}

func refuse(out *Outcome, format string, args ...any) Outcome {
	out.Status = StatusRefused
	out.Undo = map[string]string{}
	out.logf(format, args...)
	return *out
}

func applyRecordAdd(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	domain := v["domain"]
	r := tencent.Record{Name: v["subdomain"], Type: v["type"], Value: v["value"], Line: v["line"], Remark: v["remark"]}
	ttl, _ := strconv.ParseUint(v["ttl"], 10, 64)
	r.TTL = ttl
	if r.Type == "MX" {
		r.MX = 10
		if n, err := strconv.ParseUint(v["mx"], 10, 64); err == nil && n > 0 {
			r.MX = n
		}
	}
	name := fullName(r.Name, domain)
	same, err := c.Records(ctx, domain, r.Name)
	if err != nil {
		return refuse(out, "读取 %s 的解析记录失败：%v", domain, err)
	}
	for _, o := range same {
		if o.Name != r.Name || o.Line != r.Line {
			continue
		}
		if o.Type == r.Type && sameValue(o.Value, r.Value) {
			report("%s 已经有这条记录（%s），不需要添加", name, recordLabel(o))
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
		addr := func(t string) bool { return t == "A" || t == "AAAA" || t == "CNAME" }
		if (r.Type == "CNAME" && addr(o.Type)) || (o.Type == "CNAME" && addr(r.Type)) {
			return refuse(out, "%s 在「%s」线路已经有 %s，CNAME 不能和 A、AAAA 或别的 CNAME 同时存在。要换掉它，请修改那条记录", name, r.Line, recordLabel(o))
		}
	}
	report("正在给 %s 添加 %s", name, recordLabel(r))
	id, err := c.CreateRecord(ctx, domain, r)
	if err != nil {
		return refuse(out, "添加失败：%v", err)
	}
	out.Undo["domain"], out.Undo["created"] = domain, strconv.FormatUint(id, 10)
	report("完成：已添加 %s %s，按 TTL 几分钟内在各地生效", name, recordLabel(r))
	out.Status = StatusDone
	return *out
}

func applyRecordModify(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	domain := v["domain"]
	id, _ := strconv.ParseUint(v["record_id"], 10, 64)
	orig, ok, err := findRecord(ctx, c, domain, id)
	switch {
	case err != nil:
		return refuse(out, "%v", err)
	case !ok:
		return refuse(out, "%s 里已经没有编号 %d 的记录（可能在别处被删了），没有修改", domain, id)
	case orig.DefaultNS:
		return refuse(out, "这是 DNSPod 给域名自带的 NS 记录，不能修改")
	}
	want := orig // keeps the status and weight
	want.Name, want.Type, want.Value = v["subdomain"], v["type"], v["value"]
	if v["line"] != "" {
		want.Line = v["line"]
	}
	if n, err := strconv.ParseUint(v["ttl"], 10, 64); err == nil && n > 0 {
		want.TTL = n
	}
	if v["remark"] != "" {
		want.Remark = v["remark"]
	}
	if want.Type == "MX" {
		if n, err := strconv.ParseUint(v["mx"], 10, 64); err == nil && n > 0 {
			want.MX = n
		} else if want.MX == 0 {
			want.MX = 10
		}
	} else {
		want.MX = 0
	}
	if want.Name == orig.Name && want.Type == orig.Type && sameValue(want.Value, orig.Value) && want.Line == orig.Line &&
		want.TTL == orig.TTL && want.MX == orig.MX && want.Remark == orig.Remark {
		report("%s 已经是 %s，不需要修改", fullName(orig.Name, domain), recordLabel(orig))
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	report("正在把 %s 的 %s 改为 %s %s", fullName(orig.Name, domain), recordLabel(orig), fullName(want.Name, domain), recordLabel(want))
	if err := c.ModifyRecord(ctx, domain, want); err != nil {
		return refuse(out, "修改失败：%v", err)
	}
	data, _ := json.Marshal(orig)
	out.Undo["domain"], out.Undo["modified"] = domain, string(data)
	report("完成，按 TTL 几分钟内在各地生效")
	out.Status = StatusDone
	return *out
}

func applyRecordDelete(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	domain := v["domain"]
	id, _ := strconv.ParseUint(v["record_id"], 10, 64)
	orig, ok, err := findRecord(ctx, c, domain, id)
	switch {
	case err != nil:
		return refuse(out, "%v", err)
	case !ok:
		report("%s 里已经没有编号 %d 的记录，不需要删除", domain, id)
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	case orig.DefaultNS:
		return refuse(out, "这是 DNSPod 给域名自带的 NS 记录，不能删除")
	}
	report("正在删除 %s 的 %s", fullName(orig.Name, domain), recordLabel(orig))
	if err := c.DeleteRecord(ctx, domain, id); err != nil {
		return refuse(out, "删除失败：%v", err)
	}
	data, _ := json.Marshal([]tencent.Record{orig})
	out.Undo["domain"], out.Undo["deleted"] = domain, string(data)
	report("完成：已删除，按 TTL 几分钟内在各地失效")
	out.Status = StatusDone
	return *out
}

func applyRecordStatus(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	domain := v["domain"]
	id, _ := strconv.ParseUint(v["record_id"], 10, 64)
	want := strings.ToUpper(v["status"])
	orig, ok, err := findRecord(ctx, c, domain, id)
	switch {
	case err != nil:
		return refuse(out, "%v", err)
	case !ok:
		return refuse(out, "%s 里已经没有编号 %d 的记录", domain, id)
	case orig.DefaultNS:
		return refuse(out, "这是 DNSPod 给域名自带的 NS 记录，不能暂停")
	}
	verb := map[string]string{"DISABLE": "暂停", "ENABLE": "启用"}[want]
	name := fullName(orig.Name, domain)
	if orig.Status == want {
		report("%s 的 %s 已经是%s状态，不需要修改", name, recordLabel(orig), verb)
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	report("正在%s %s 的 %s", verb, name, recordLabel(orig))
	if err := c.SetRecordStatus(ctx, domain, id, want); err != nil {
		return refuse(out, "%s失败：%v", verb, err)
	}
	out.Undo["domain"], out.Undo["record_id"], out.Undo["status"] = domain, v["record_id"], orig.Status
	report("完成：已%s，按 TTL 几分钟内在各地生效", verb)
	out.Status = StatusDone
	return *out
}

func undoRecordStatus(ctx context.Context, c *tencent.Client, undo map[string]string) error {
	id, _ := strconv.ParseUint(undo["record_id"], 10, 64)
	status := undo["status"]
	if status == "" {
		status = "ENABLE"
	}
	return c.SetRecordStatus(ctx, undo["domain"], id, status)
}
