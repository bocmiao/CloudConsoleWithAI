package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// What the pages show that the AI could not read before: a server's
// databases, what was changed lately, and the notices.

// toolPanelDatabases lists a 1Panel server's MySQL and MariaDB databases.
func (a *App) toolPanelDatabases(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg serverArg
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	if msg := a.noPanel(arg.ServerID, false); msg != "" {
		return msg, nil
	}
	list, _, err := a.DatabasesPage(ctx, arg.ServerID, PageWait)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "这台服务器的 1Panel 里没有安装 MySQL 或 MariaDB。", nil
	}
	var b strings.Builder
	for _, ap := range list {
		state := "运行中"
		if !ap.Running {
			state = "没有运行"
		}
		fmt.Fprintf(&b, "%s（%s %s，%s）：", ap.App, ap.Kind, ap.Version, state)
		if ap.Error != "" {
			fmt.Fprintf(&b, "读取数据库失败：%s\n", ap.Error)
			continue
		}
		if len(ap.Databases) == 0 {
			b.WriteString("还没有数据库\n")
			continue
		}
		fmt.Fprintf(&b, "%d 个数据库\n", len(ap.Databases))
		for _, d := range ap.Databases {
			fmt.Fprintf(&b, "- %s 用户=%s 允许从=%s", d.Name, orDash(d.User), accessText(d.Access))
			if d.CreatedAt != "" {
				fmt.Fprintf(&b, " 创建于 %s", d.CreatedAt[:min(10, len(d.CreatedAt))])
			}
			if d.Note != "" {
				fmt.Fprintf(&b, " 备注=%s", d.Note)
			}
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

func accessText(access string) string {
	switch access {
	case "%":
		return "任何地址"
	case "localhost", "":
		return "只有本机"
	}
	return access
}

var execStatusText = map[string]string{
	actions.StatusDone: "成功", actions.StatusRefused: "没有执行", actions.StatusRolledBack: "失败，已自动恢复",
	actions.StatusFailed: "失败", actions.StatusUndone: "已撤销", store.ExecRunning: "正在执行", store.ExecInterrupted: "被中断",
}

var planStatusText = map[string]string{
	core.PlanProposed: "等待用户确认", core.PlanRunning: "正在执行", core.PlanDone: "已执行", core.PlanPartial: "部分执行",
}

var originText = map[string]string{OriginUser: "用户", OriginAI: "AI 检查", OriginPlan: "清单", OriginAuto: "自动"}

// toolRecentChanges is what was changed lately, and the checklists
// still waiting.
func (a *App) toolRecentChanges(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		ServerID int64 `json:"server_id"`
		Limit    int   `json:"limit"`
		ID       int64 `json:"id"`
		PlanID   int64 `json:"plan_id"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	if arg.ID > 0 {
		return a.execDetailText(arg.ID)
	}
	if arg.PlanID > 0 {
		return a.planDetailText(arg.PlanID)
	}
	if arg.Limit <= 0 || arg.Limit > 50 {
		arg.Limit = 20
	}
	var b strings.Builder
	logs, err := a.Store.ListServerExec(arg.ServerID, true, arg.Limit)
	if err != nil {
		return "", err
	}
	b.WriteString("最近的修改（新的在前）：\n")
	if len(logs) == 0 {
		b.WriteString("- 没有\n")
	}
	for _, e := range logs {
		status := execStatusText[e.Status]
		if status == "" {
			status = e.Status
		}
		fmt.Fprintf(&b, "- #%d %s %s", e.ID, e.StartedAt, e.Title)
		if e.ServerName != "" {
			fmt.Fprintf(&b, "（%s）", e.ServerName)
		}
		fmt.Fprintf(&b, " 结果=%s 发起=%s", status, orDash(originText[e.Origin]))
		if e.Capability != "" {
			fmt.Fprintf(&b, " 操作=%s", e.Capability)
		}
		if e.Kind == store.ExecRollback {
			fmt.Fprintf(&b, " （撤销 #%d）", e.UndoOf)
		}
		if e.UndoneBy > 0 {
			fmt.Fprintf(&b, " 已被 #%d 撤销", e.UndoneBy)
		}
		if e.Status == actions.StatusFailed || e.Status == actions.StatusRolledBack || e.Status == actions.StatusRefused {
			// The list leaves the output out; the last line says why.
			if full, err := a.Store.GetExec(e.ID); err == nil && lastLine(full.Output) != "" {
				fmt.Fprintf(&b, " 原因：%s", lastLine(full.Output))
			}
		}
		b.WriteString("\n")
	}
	if plans, err := a.Store.ListPlans(30); err == nil {
		var waiting []string
		for _, p := range plans {
			if p.Status == core.PlanProposed && (arg.ServerID == 0 || p.ServerID == arg.ServerID) {
				waiting = append(waiting, fmt.Sprintf("- 清单 #%d %s（%s 提出，%s）", p.ID, p.Title, p.CreatedAt, planStatusText[p.Status]))
			}
		}
		if len(waiting) > 0 {
			b.WriteString("\n还没有执行的清单：\n" + strings.Join(waiting, "\n") + "\n")
		}
	}
	if arg.ServerID == 0 {
		if list, err := a.Store.ListAudit(20); err == nil && len(list) > 0 {
			b.WriteString("\n最近的设置变更：\n")
			for _, e := range list {
				fmt.Fprintf(&b, "- %s %s %s", e.At, e.Action, e.Target)
				if e.Detail != "" {
					fmt.Fprintf(&b, "（%s）", e.Detail)
				}
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n看某一步的完整输出用 id（# 后面的编号），看一份清单每一步的情况用 plan_id。撤销某一步请用户在「操作记录」里点撤销；你不能直接撤销。")
	return b.String(), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// toolReminders is the 通知 page: the latest reports and alerts.
func (a *App) toolReminders(_ context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Limit int `json:"limit"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	if arg.Limit <= 0 || arg.Limit > 20 {
		arg.Limit = 8
	}
	v := a.Notices()
	var b strings.Builder
	s := v.Settings
	fmt.Fprintf(&b, "通知设置：日报 %s（%s）；提醒：高风险 IP %s、敏感文件被下载 %s、证书 %s、续费和余额 %s、云监控告警 %s、自动封禁 %s；推送 %s\n",
		kaiGuan(s.Daily), s.DailyAt, kaiGuan(s.AlertRisk), kaiGuan(s.AlertLeak), kaiGuan(s.AlertCert), kaiGuan(s.AlertRenew), kaiGuan(s.AlertCloud),
		kaiGuan(s.AlertBlock), orDash(s.WebhookKind))
	fmt.Fprintf(&b, "未读 %d 条。最近的通知（新的在前）：\n", v.Unread)
	if len(v.Notices) == 0 {
		b.WriteString("- 没有\n")
	}
	for i, n := range v.Notices {
		if i >= arg.Limit {
			break
		}
		read := ""
		if !n.Read {
			read = "（未读）"
		}
		text := []rune(n.Text)
		if len(text) > 1500 {
			text = append(text[:1500], []rune("…")...)
		}
		fmt.Fprintf(&b, "\n### %s %s%s\n%s\n", n.At, n.Title, read, string(text))
	}
	return b.String(), nil
}

func kaiGuan(on bool) string {
	if on {
		return "开"
	}
	return "关"
}

// execDetailText is one step of the execution log in full: what it ran
// and what it said.
func (a *App) execDetailText(id int64) (string, error) {
	e, err := a.Store.GetExec(id)
	if err != nil {
		return "", userErr("操作记录里没有 #%d", id)
	}
	var b strings.Builder
	status := execStatusText[e.Status]
	if status == "" {
		status = e.Status
	}
	fmt.Fprintf(&b, "#%d %s\n时间：%s ~ %s\n服务器：%s\n结果：%s（发起：%s）\n", e.ID, e.Title, e.StartedAt, orDash(e.FinishedAt), orDash(e.ServerName), status, orDash(originText[e.Origin]))
	if e.Note != "" {
		fmt.Fprintf(&b, "说明：%s\n", e.Note)
	}
	if e.Capability != "" {
		params, _ := json.Marshal(e.Params)
		fmt.Fprintf(&b, "操作：%s（%s）参数：%s\n", e.Capability, orDash(e.Via), clipText(redactText(string(params)), 3000))
	}
	if e.PlanID > 0 {
		fmt.Fprintf(&b, "属于清单 #%d 第 %d 步\n", e.PlanID, e.StepIdx+1)
	}
	switch {
	case e.Kind == store.ExecRollback:
		fmt.Fprintf(&b, "这是对 #%d 的撤销\n", e.UndoOf)
	case e.UndoneBy > 0:
		fmt.Fprintf(&b, "已被 #%d 撤销\n", e.UndoneBy)
	case e.Reversible && e.Status == actions.StatusDone:
		b.WriteString("可以在「操作记录」里撤销\n")
	}
	if e.BackupDir != "" {
		fmt.Fprintf(&b, "修改前的备份：%s\n", e.BackupDir)
	}
	if e.Commands != "" {
		fmt.Fprintf(&b, "\n执行的命令或接口请求：\n%s\n", clipText(redactText(e.Commands), 6000))
	}
	fmt.Fprintf(&b, "\n输出：\n%s\n", orDash(clipText(redactText(e.Output), 12000)))
	return b.String(), nil
}

// planDetailText is a checklist and how each of its steps went.
func (a *App) planDetailText(id int64) (string, error) {
	v, err := a.Plan(id)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	status := planStatusText[v.Status]
	if status == "" {
		status = v.Status
	}
	fmt.Fprintf(&b, "清单 #%d %s（%s 提出，%s）\n原因：%s\n", v.ID, v.Title, v.CreatedAt, status, v.Reason)
	for i, st := range v.StepList {
		state := execStatusText[st.Status]
		switch {
		case st.Status == "":
			state = "没有执行"
		case st.Status == "skipped":
			state = "跳过"
		case st.Status == "queued":
			state = "排队"
		case state == "":
			state = st.Status
		}
		fmt.Fprintf(&b, "\n%d. %s %s：%s", i+1, st.Capability, st.Summary, state)
		if !st.Executable && st.Blocked != "" {
			fmt.Fprintf(&b, "（不能执行：%s）", st.Blocked)
		}
		if st.LogID > 0 {
			fmt.Fprintf(&b, " 操作记录 #%d", st.LogID)
		}
		b.WriteString("\n")
		if n := len(st.Log); n > 0 {
			b.WriteString(clipText(redactText(strings.Join(st.Log[max(0, n-8):], "\n")), 2000) + "\n")
		}
	}
	return b.String(), nil
}

// toolMiaoPanel is Miao Panel itself: its version, what is set up and
// switched on, and what automatic blocking is doing.
func (a *App) toolMiaoPanel(ctx context.Context, _ json.RawMessage) (string, error) {
	var b strings.Builder
	u := a.UpdateStatus()
	fmt.Fprintf(&b, "Miao Panel 版本 %s（%s）", u.Current, u.OS)
	switch {
	case u.Newer && u.Latest != nil:
		fmt.Fprintf(&b, "，有新版本 %s，", u.Latest.Version)
		if u.CanApply {
			b.WriteString("用户可以在「设置 → 版本和诊断」点「更新到最新版本」")
		} else {
			b.WriteString("要手动下载替换（" + u.Why + "）")
		}
	case u.Latest != nil:
		b.WriteString("，已经是最新版本")
	case !u.Enabled:
		b.WriteString("，没有开启每天检查更新")
	}
	b.WriteString("\n")
	servers, _ := a.Store.ListServers()
	fmt.Fprintf(&b, "服务器 %d 台；腾讯云密钥 %s；阿里云密钥 %s\n", len(servers), yesNo(a.tencentClient() != nil), yesNo(a.aliyunClient() != nil))
	if s, err := a.AISettings(); err == nil {
		fmt.Fprintf(&b, "AI 模型：%s", s.Model)
		if spent, err := a.Store.MonthCost(); err == nil {
			fmt.Fprintf(&b, "，本月花费 %.2f %s", spent[s.Currency], s.Currency)
		}
		if s.MonthlyBudget > 0 {
			fmt.Fprintf(&b, "（上限 %.2f）", s.MonthlyBudget)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "AI 自由命令（free_command）：%s", kaiGuan(a.freeEnabled()))
	if !a.freeEnabled() {
		b.WriteString("（要用户在「设置 → AI 自由命令」开启，你不能开）")
	}
	b.WriteString("\n")
	_, mail := a.mailConfig()
	_, sms := a.smsConfig()
	fmt.Fprintf(&b, "邮件发送 %s，短信发送 %s，通知推送 webhook %s，预先加载页面数据 %s\n", yesNo(mail), yesNo(sms), orDash(a.Notices().Settings.WebhookKind), kaiGuan(a.PreloadOn()))
	st := a.AutoBlock()
	s := st.Settings
	fmt.Fprintf(&b, "\n自动封禁：%s", kaiGuan(s.Enabled))
	if s.Enabled {
		fmt.Fprintf(&b, "，%s", autoRuleText(s))
	}
	if len(s.Allow) > 0 {
		b.WriteString("；白名单：" + strings.Join(s.Allow, "、"))
	}
	if st.LastRun != "" {
		fmt.Fprintf(&b, "；上次运行 %s %s", st.LastRun, st.LastNote)
	}
	b.WriteString("\n")
	if len(st.Blocked) > 0 {
		fmt.Fprintf(&b, "自动封禁中的 IP（%d 个）：\n", len(st.Blocked))
		for _, x := range st.Blocked {
			until := "手动解封前一直封禁"
			if x.Until != "" {
				until = "到 " + x.Until
			}
			fmt.Fprintf(&b, "- %s 站点 %s，%s 封禁，%s：%s\n", x.IP, x.Zone, x.At, until, x.Reason)
		}
	}
	if n := len(st.Events); n > 0 {
		b.WriteString("自动封禁最近做的事：\n")
		for _, e := range st.Events[max(0, n-10):] {
			fmt.Fprintf(&b, "- %s %s\n", e.At, e.Text)
		}
	}
	b.WriteString("\n修改监控、自动封禁、通知的设置用 monitor.settings.set、autoblock.set、notice.settings.set（server_id 填 0）。账号、密钥、推送地址、更新只能用户自己在「设置」里改。")
	return b.String(), nil
}

func yesNo(b bool) string {
	if b {
		return "已配置"
	}
	return "没有配置"
}

// toolBlockedIPs is every IP Miao Panel blocked in EdgeOne.
func (a *App) toolBlockedIPs(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Refresh bool `json:"refresh"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	read := PageWait
	if arg.Refresh {
		read = PageRefresh
	}
	zones, _, err := a.BlockedPage(ctx, read)
	if err != nil {
		return "", err
	}
	if a.tencentClient() == nil {
		return "还没有配置腾讯云密钥，没有 EdgeOne 封禁。", nil
	}
	auto := map[string]AutoBlocked{}
	for _, x := range a.AutoBlock().Blocked {
		auto[x.Zone+" "+x.IP] = x
	}
	var b strings.Builder
	if len(zones) == 0 {
		b.WriteString("EdgeOne 的站点里没有 Miao Panel 封禁的 IP。\n")
	}
	for _, z := range zones {
		fmt.Fprintf(&b, "站点 %s 封禁了 %d 个：\n", z.Zone, len(z.IPs))
		for _, ip := range z.IPs {
			b.WriteString("- " + ip)
			if x, ok := auto[z.Zone+" "+ip]; ok {
				until := "手动解封前一直封禁"
				if x.Until != "" {
					until = "到 " + x.Until + " 自动解封"
				}
				fmt.Fprintf(&b, "（自动封禁，%s；%s）", until, x.Reason)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("解封用 eo.ip.unblock（domain 填站点，ips 填要解封的）。")
	return b.String(), nil
}
