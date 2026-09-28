package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
)

// ---- 阿里云 云解析 (Alidns) records ----
//
// The same edits as for DNSPod: add, change, delete, pause or resume one
// record, and "make this name point there", which replaces the name's
// default-line A, AAAA and CNAME records. Lines are Alidns codes
// (default, telecom, …).

// AliLineCode turns a line's name (默认, 电信) or code into its code.
func AliLineCode(line string) string {
	if line == "" {
		return aliyun.DefaultLine
	}
	for _, l := range aliyun.Lines {
		if l.Name == line || l.Code == line {
			return l.Code
		}
	}
	return line
}

// AliLineName turns a line code into its name, for reading.
func AliLineName(code string) string {
	for _, l := range aliyun.Lines {
		if l.Code == code {
			return l.Name
		}
	}
	return code
}

// aliLine turns a line's name or code into its code, asking the domain's
// edition for the lines beyond the usual ones (中国地区_西北 and so on).
func aliLine(ctx context.Context, c *aliyun.Client, domain, line string) string {
	for _, l := range aliyun.Lines {
		if line == "" || l.Name == line || l.Code == line {
			return AliLineCode(line)
		}
	}
	if info, err := c.DomainInfo(ctx, domain); err == nil {
		for _, l := range info.Lines {
			if l.Name == line || l.Code == line {
				return l.Code
			}
		}
	}
	return line
}

// gone and there say an undo already happened, in part: the record it
// deletes is not there, or the one it adds back is.
func gone(err error) bool  { return err == nil || aliyun.IsCode(err, "DomainRecordNotBelongToUser") }
func there(err error) bool { return err == nil || aliyun.IsCode(err, "DomainRecordDuplicate") }

// aliPutBack adds deleted records back, saying which could not be.
func aliPutBack(ctx context.Context, c *aliyun.Client, domain string, removed []aliyun.Record) error {
	var lost []string
	for _, r := range removed {
		r.ID = ""
		if _, err := c.AddRecord(ctx, domain, r); !there(err) {
			lost = append(lost, fmt.Sprintf("%s（%v）", aliRecordText(r), err))
		}
	}
	if len(lost) > 0 {
		return fmt.Errorf("没能加回：%s，请在阿里云控制台手动添加", strings.Join(lost, "；"))
	}
	return nil
}

func aliRecordText(r aliyun.Record) string {
	s := r.Type + " " + r.Value
	if r.Type == "MX" {
		s = fmt.Sprintf("MX %d %s", r.Priority, r.Value)
	}
	if r.Line != "" && r.Line != aliyun.DefaultLine {
		s += "（" + AliLineName(r.Line) + "）"
	}
	return s
}

func aliRecordOf(v map[string]string) aliyun.Record {
	r := aliyun.Record{Name: v["subdomain"], Type: strings.ToUpper(v["type"]), Value: v["value"], Line: AliLineCode(v["line"]), Remark: v["remark"]}
	r.TTL, _ = strconv.Atoi(v["ttl"])
	r.Priority, _ = strconv.Atoi(v["mx"])
	if r.Type == "MX" && r.Priority == 0 {
		r.Priority = 10
	}
	return r
}

func aliFindRecord(ctx context.Context, c *aliyun.Client, domain, id string) (aliyun.Record, error) {
	all, err := c.Records(ctx, domain)
	if err != nil {
		return aliyun.Record{}, fmt.Errorf("读取 %s 的解析记录失败：%w", domain, err)
	}
	for _, r := range all {
		if r.ID == id {
			return r, nil
		}
	}
	return aliyun.Record{}, fmt.Errorf("%s 里找不到记录 %s，可能已经在别处删除了", domain, id)
}

func aliDNSApply(ctx context.Context, env *Env, op string, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Aliyun
	domain := strings.ToLower(v["domain"])
	save := func(r aliyun.Record) string { b, _ := json.Marshal(r); return string(b) }
	switch op {
	case "ali_dns_add":
		r := aliRecordOf(v)
		r.Line = aliLine(ctx, c, domain, v["line"])
		name := fullName(r.Name, domain)
		all, err := c.Records(ctx, domain)
		if err != nil {
			return refused(out, "读取 %s 的解析记录失败：%v", domain, err)
		}
		for _, x := range all {
			if x.Name == r.Name && x.Type == r.Type && sameValue(x.Value, r.Value) && x.Line == r.Line {
				report("%s 已经有 %s，不需要添加", name, aliRecordText(x))
				out.Status, out.Undo = StatusDone, map[string]string{}
				return *out
			}
		}
		report("正在给 %s 添加 %s", name, aliRecordText(r))
		id, err := c.AddRecord(ctx, domain, r)
		if err != nil && id == "" {
			return refused(out, "添加失败：%v", err)
		}
		out.Undo["domain"], out.Undo["added"] = domain, id
		out.Result = map[string]string{"record_id": id}
		if err != nil {
			report("记录已添加，但备注没有设置成：%v", err)
		}
		report("完成：已添加，按 TTL 几分钟内在各地生效")
	case "ali_dns_modify":
		old, err := aliFindRecord(ctx, c, domain, v["record_id"])
		if err != nil {
			return refused(out, "%v", err)
		}
		r := old
		n := aliRecordOf(v)
		r.Name, r.Type, r.Value = n.Name, n.Type, n.Value
		if v["line"] != "" {
			r.Line = aliLine(ctx, c, domain, v["line"])
		}
		if v["ttl"] != "" {
			r.TTL = n.TTL
		}
		if r.Type == "MX" {
			r.Priority = n.Priority
		}
		remark := old.Remark
		switch {
		case v["clear_remark"] == "yes":
			remark = ""
		case v["remark"] != "":
			remark = n.Remark
		}
		report("正在把 %s 的 %s 改为 %s", fullName(old.Name, domain), aliRecordText(old), aliRecordText(r))
		// The record first, the remark after: a remark that does not take
		// does not undo the change.
		if err := c.UpdateRecord(ctx, r); err != nil {
			return refused(out, "修改失败：%v", err)
		}
		out.Undo["domain"], out.Undo["before"] = domain, save(old)
		if remark != old.Remark {
			if err := c.SetRecordRemark(ctx, r.ID, remark); err != nil {
				report("记录已修改，但备注没有改成：%v", err)
			}
		}
		report("完成：已修改，按 TTL 几分钟内在各地生效")
	case "ali_dns_delete":
		old, err := aliFindRecord(ctx, c, domain, v["record_id"])
		if err != nil {
			return refused(out, "%v", err)
		}
		report("正在删除 %s 的 %s", fullName(old.Name, domain), aliRecordText(old))
		if err := c.DeleteRecord(ctx, old.ID); err != nil {
			return refused(out, "删除失败：%v", err)
		}
		out.Undo["domain"], out.Undo["deleted"] = domain, save(old)
		report("完成：已删除，按 TTL 几分钟内在各地失效")
	case "ali_dns_status":
		old, err := aliFindRecord(ctx, c, domain, v["record_id"])
		if err != nil {
			return refused(out, "%v", err)
		}
		on := v["status"] == "enable"
		if (old.Status != "disabled") == on {
			report("%s 的 %s 已经是%s状态", fullName(old.Name, domain), aliRecordText(old), map[bool]string{true: "启用", false: "暂停"}[on])
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
		report("正在%s %s 的 %s", map[bool]string{true: "启用", false: "暂停"}[on], fullName(old.Name, domain), aliRecordText(old))
		if err := c.SetRecordStatus(ctx, old.ID, on); err != nil {
			return refused(out, "操作失败：%v", err)
		}
		out.Undo["domain"], out.Undo["record_id"], out.Undo["enabled"] = domain, old.ID, strconv.FormatBool(!on)
		report("完成")
	case "ali_dns_set":
		// Point a name somewhere: its default-line A, AAAA and CNAME records
		// give way to the one wanted.
		sub, typ, value := v["subdomain"], strings.ToUpper(v["type"]), v["value"]
		name := fullName(sub, domain)
		if err := checkRecordValue(typ, value); err != nil {
			return refused(out, "%v", err)
		}
		all, err := c.Records(ctx, domain)
		if err != nil {
			return refused(out, "读取 %s 的解析记录失败：%v", domain, err)
		}
		var old []aliyun.Record
		for _, r := range all {
			if r.Name != sub || r.Line != aliyun.DefaultLine || (r.Type != "A" && r.Type != "AAAA" && r.Type != "CNAME") {
				continue
			}
			old = append(old, r)
		}
		if len(old) == 1 && old[0].Type == typ && sameValue(old[0].Value, value) {
			report("%s 已经解析到 %s %s，不需要修改", name, typ, value)
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
		var removed []aliyun.Record
		rollBack := func(what string, err error) Outcome {
			if backErr := aliPutBack(ctx, c, domain, removed); backErr != nil {
				out.Status = StatusFailed
				out.logf("%s：%v；%v", what, err, backErr)
				return *out
			}
			out.Status = StatusRolledBack
			out.logf("%s：%v（删掉的已加回，和原来一样）", what, err)
			return *out
		}
		for _, r := range old {
			report("正在删除 %s 原来的 %s", name, aliRecordText(r))
			if err := c.DeleteRecord(ctx, r.ID); err != nil {
				return rollBack("删除原来的记录失败", err)
			}
			removed = append(removed, r)
		}
		ttl, _ := strconv.Atoi(v["ttl"])
		report("正在添加 %s 的 %s %s", name, typ, value)
		id, err := c.AddRecord(ctx, domain, aliyun.Record{Name: sub, Type: typ, Value: value, Line: aliyun.DefaultLine, TTL: ttl})
		if err != nil {
			return rollBack("添加失败", err)
		}
		b, _ := json.Marshal(removed)
		out.Undo["domain"], out.Undo["added"], out.Undo["removed"] = domain, id, string(b)
		report("完成：%s 现在解析到 %s %s，按 TTL 几分钟内在各地生效", name, typ, value)
	default:
		out.Status = StatusFailed
		out.logf("未知的阿里云操作 %s", op)
		return *out
	}
	out.Status = StatusDone
	return *out
}

func aliDNSUndo(ctx context.Context, c *aliyun.Client, op string, undo map[string]string) error {
	domain := undo["domain"]
	readBack := func(key string) (aliyun.Record, error) {
		var r aliyun.Record
		err := json.Unmarshal([]byte(undo[key]), &r)
		return r, err
	}
	switch op {
	case "ali_dns_add":
		if err := c.DeleteRecord(ctx, undo["added"]); !gone(err) {
			return err
		}
		return nil
	case "ali_dns_modify":
		r, err := readBack("before")
		if err != nil {
			return err
		}
		if err := c.UpdateRecord(ctx, r); err != nil {
			return err
		}
		return c.SetRecordRemark(ctx, r.ID, r.Remark)
	case "ali_dns_delete":
		r, err := readBack("deleted")
		if err != nil {
			return err
		}
		r.ID = ""
		// AddRecord pauses it again when it was paused.
		if _, err := c.AddRecord(ctx, domain, r); !there(err) {
			return err
		}
		return nil
	case "ali_dns_status":
		return c.SetRecordStatus(ctx, undo["record_id"], undo["enabled"] == "true")
	case "ali_dns_set":
		// Each part may have happened in an earlier try.
		if err := c.DeleteRecord(ctx, undo["added"]); !gone(err) {
			return err
		}
		var removed []aliyun.Record
		if err := json.Unmarshal([]byte(undo["removed"]), &removed); err != nil {
			return err
		}
		return aliPutBack(ctx, c, domain, removed)
	}
	return fmt.Errorf("未知的阿里云操作 %s", op)
}

func init() {
	domain := Param{Name: "domain", Kind: "host", Required: true, Desc: "阿里云云解析里的主域名，例如 example.com"}
	recordID := Param{Name: "record_id", Kind: "name", Required: true, Desc: "记录编号（aliyun_dns 返回的 id）"}
	fields := func(add bool) []Param {
		ps := []Param{
			{Name: "subdomain", Kind: "subdomain", Required: true, Desc: "主机记录，例如 www、mail；主域名本身填 @，泛解析填 *"},
			{Name: "type", Kind: "enum", Enum: RecordTypes, Required: true, Desc: "记录类型"},
			{Name: "value", Kind: "text", Required: true, Desc: recordValueDesc},
			{Name: "line", Kind: "text", Desc: "解析线路：默认、电信、联通、移动、教育网、境外"},
			{Name: "ttl", Kind: "int", Min: 1, Max: 86400, Desc: "TTL（秒），免费版最小 600"},
			{Name: "mx", Kind: "int", Min: 1, Max: 50, Desc: "MX 优先级，1 到 50，数字越小越优先（MX 记录用）"},
			{Name: "remark", Kind: "text", Desc: "备注"},
		}
		if !add {
			ps = append(ps, Param{Name: "clear_remark", Kind: "enum", Enum: []string{"yes"}, Desc: "yes 清空备注"})
		}
		if add {
			ps[3].Desc += "，不填是默认"
			ps[4].Default, ps[4].Desc = "600", ps[4].Desc+"，不填是 600"
			ps[5].Desc += "，不填是 10"
		} else {
			ps[3].Desc += "，不填保持原样"
			ps[4].Desc += "，不填保持原样"
			ps[6].Desc += "，不填保持原样"
		}
		return ps
	}
	impl := func(op, downtime, undo string) map[string]Impl {
		return map[string]Impl{"*": {Via: "阿里云接口", Cloud: op, Downtime: downtime, Undo: undo}}
	}
	register(&Capability{
		Name: "aliyun.dns.record.add", Title: "添加阿里云解析记录", Risk: core.R2, Reversible: true,
		Params: append([]Param{domain}, fields(true)...),
		Impls:  impl("ali_dns_add", "按 TTL 几分钟内在各地生效", "删除这条记录"),
		Check:  checkRecordParams,
	})
	register(&Capability{
		Name: "aliyun.dns.record.modify", Title: "修改阿里云解析记录", Risk: core.R2, Reversible: true,
		Params: append([]Param{domain, recordID}, fields(false)...),
		Impls:  impl("ali_dns_modify", "按 TTL 几分钟内在各地生效", "把这条记录改回修改前的样子"),
		Check:  checkRecordParams,
	})
	register(&Capability{
		Name: "aliyun.dns.record.delete", Title: "删除阿里云解析记录", Risk: core.R2, Reversible: true,
		Params: []Param{domain, recordID},
		Impls:  impl("ali_dns_delete", "按 TTL 几分钟内在各地失效", "把删除的记录加回来（记录编号会变）"),
	})
	register(&Capability{
		Name: "aliyun.dns.record.status", Title: "暂停或启用阿里云解析记录", Risk: core.R2, Reversible: true,
		Params: []Param{domain, recordID,
			{Name: "status", Kind: "enum", Enum: []string{"disable", "enable"}, Required: true, Desc: "disable 暂停（记录保留但不生效），enable 启用"}},
		Impls: impl("ali_dns_status", "按 TTL 几分钟内在各地生效", "恢复原来的状态"),
	})
	register(&Capability{
		Name: "aliyun.dns.record.set", Title: "设置阿里云解析", Risk: core.R2, Reversible: true,
		Params: []Param{domain,
			{Name: "subdomain", Kind: "subdomain", Required: true, Desc: "主机记录，例如 www；主域名本身填 @"},
			{Name: "type", Kind: "enum", Enum: []string{"A", "AAAA", "CNAME"}, Required: true, Desc: "A 指向 IPv4，AAAA 指向 IPv6，CNAME 指向另一个域名"},
			{Name: "value", Kind: "text", Required: true, Desc: "IP 地址或域名"},
			{Name: "ttl", Kind: "int", Min: 1, Max: 86400, Default: "600", Desc: "TTL（秒），免费版最小 600"}},
		Impls: impl("ali_dns_set", "按 TTL 几分钟内在各地生效；这个名字原来默认线路的 A、AAAA、CNAME 记录会被替换", "删除新记录，把原来的记录加回来"),
		Check: func(v map[string]string) error { return CheckRecord(v["type"], v["value"]) },
	})
}
