package actions

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
)

// ---- 阿里云 CDN: refreshing and prefetching cached content ----

// aliCDNTargets checks every address belongs to an accelerated domain of
// the account.
func aliCDNTargets(ctx context.Context, c *aliyun.Client, targets []string) ([]string, error) {
	doms, err := c.CDNDomains(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取 CDN 加速域名失败：%w", err)
	}
	have := map[string]bool{}
	for _, d := range doms {
		have[strings.ToLower(d.Name)] = true
	}
	var out []string
	for _, t := range targets {
		u, err := url.Parse(t)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%q 要是完整的网址，例如 https://cdn.example.com/css/app.css", t)
		}
		host := strings.ToLower(u.Hostname())
		if !have[host] {
			return nil, fmt.Errorf("%s 不是这个阿里云账号的 CDN 加速域名", host)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有要处理的网址")
	}
	return out, nil
}

func aliCDNApply(ctx context.Context, env *Env, op string, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Aliyun
	targets, err := aliCDNTargets(ctx, c, splitTargets(v["targets"]))
	if err != nil {
		return refused(out, "%v", err)
	}
	var ids []string
	if op == "ali_cdn_purge" {
		dir := v["type"] == "dir"
		for i, t := range targets {
			if dir && !strings.HasSuffix(t, "/") {
				targets[i] = t + "/"
			}
		}
		report("正在刷新阿里云 CDN 缓存（%s）：%s", map[bool]string{true: "目录", false: "网址"}[dir], strings.Join(targets, "、"))
		ids, err = c.PurgeCDN(ctx, targets, dir)
	} else {
		report("正在预热阿里云 CDN：%s", strings.Join(targets, "、"))
		ids, err = c.PrefetchCDN(ctx, targets)
	}
	if err != nil {
		return refused(out, "提交失败：%v", err)
	}
	var tasks []aliyun.CDNTask
	done, _ := waitFor(ctx, env, 30, func() (bool, error) {
		var err error
		if tasks, err = c.CDNTasks(ctx, ids...); err != nil {
			return false, err
		}
		for _, t := range tasks {
			if t.Status == "Refreshing" {
				return false, nil
			}
		}
		return len(tasks) > 0, nil
	})
	var failed []string
	for _, t := range tasks {
		if t.Status != "Complete" && t.Status != "Refreshing" {
			failed = append(failed, fmt.Sprintf("%s（%s %s）", t.Path, t.Status, t.Reason))
		}
	}
	if len(failed) > 0 {
		out.Status = StatusFailed
		out.logf("有 %d 个没有完成：%s", len(failed), strings.Join(failed, "；"))
		return *out
	}
	if !done {
		report("已提交（任务 %s），阿里云还在处理，一般几分钟内完成", strings.Join(ids, "、"))
	} else {
		report("完成：%d 个网址已处理", len(targets))
	}
	out.Status, out.Undo = StatusDone, map[string]string{}
	return *out
}

func init() {
	register(&Capability{
		Name: "aliyun.cdn.purge", Title: "刷新阿里云 CDN 缓存", Risk: core.R2,
		NoUndo: "刷新缓存只是让节点重新从源站拉取内容，不需要也无法回滚",
		Params: []Param{
			{Name: "targets", Kind: "lines", Required: true, Desc: "要刷新的完整网址或目录（https:// 开头），多个用逗号或换行分隔；必须是这个账号的 CDN 加速域名"},
			{Name: "type", Kind: "enum", Enum: []string{"url", "dir"}, Default: "url", Desc: "url：指定网址；dir：整个目录"},
		},
		Impls: map[string]Impl{"*": {Via: "阿里云接口", Cloud: "ali_cdn_purge", Downtime: "不中断访问，刷新后的第一次访问会慢一点"}},
	})
	register(&Capability{
		Name: "aliyun.cdn.prefetch", Title: "预热阿里云 CDN", Risk: core.R1,
		NoUndo: "预热只是提前把内容缓存到节点，不需要回滚",
		Params: []Param{
			{Name: "targets", Kind: "lines", Required: true, Desc: "要预热的完整网址（https:// 开头），多个用逗号或换行分隔"},
		},
		Impls: map[string]Impl{"*": {Via: "阿里云接口", Cloud: "ali_cdn_prefetch", Downtime: "不影响访问；会从源站拉一次内容"}},
	})
}
