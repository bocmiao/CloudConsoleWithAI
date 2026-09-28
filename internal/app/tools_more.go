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
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
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
		if list, err := a.Store.ListAudit(10); err == nil && len(list) > 0 {
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
	b.WriteString("\n撤销某一步请用户在「操作记录」里点撤销；你不能直接撤销。")
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
