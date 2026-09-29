package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/btpanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// Backups of a 1Panel website: what 1Panel has kept, the daily backup
// Miao Panel set up, and 1Panel's own backup tasks that cover the site.

// BackupView is one backup of a site.
type BackupView struct {
	ID          uint   `json:"id"`
	File        string `json:"file"`
	CreatedAt   string `json:"createdAt"`
	Account     string `json:"account"` // where it is kept, in words
	Local       bool   `json:"local"`   // on the server itself
	Status      string `json:"status"`  // Success, Waiting, Failed
	Message     string `json:"message,omitempty"`
	Note        string `json:"note,omitempty"`
	Automatic   bool   `json:"automatic"` // made by a scheduled task
	BeforeWrite bool   `json:"beforeWrite,omitempty"`
}

// BackupScheduleView is a scheduled backup of a site.
type BackupScheduleView struct {
	Name       string `json:"name"`
	Time       string `json:"time"` // "03:00" for a daily one, empty otherwise
	Spec       string `json:"spec"`
	Keep       int    `json:"keep"`
	Account    string `json:"account"`
	Enabled    bool   `json:"enabled"`
	LastStatus string `json:"lastStatus,omitempty"`
	LastAt     string `json:"lastAt,omitempty"`
}

// SiteBackupsView is the 备份 section of a site.
type SiteBackupsView struct {
	Backups  []BackupView             `json:"backups"`
	Schedule *BackupScheduleView      `json:"schedule"`
	Others   []BackupScheduleView     `json:"others"` // 1Panel's own tasks covering the site
	Accounts []onepanel.BackupAccount `json:"accounts"`
}

func accountWords(accounts []onepanel.BackupAccount, typ, name string) string {
	if strings.EqualFold(typ, "LOCAL") {
		return "服务器本机"
	}
	for _, a := range accounts {
		if a.Name == name && a.Type != "" {
			return a.Name + "（" + a.Type + "）"
		}
	}
	if name != "" {
		return name
	}
	return typ
}

func scheduleOf(j onepanel.Cronjob, accounts []onepanel.BackupAccount) BackupScheduleView {
	v := BackupScheduleView{Name: j.Name, Time: actions.BackupClock(j.Spec), Spec: j.Spec, Keep: j.RetainCopies,
		Enabled: j.Status != "Disable", LastStatus: j.LastRecordStatus, LastAt: j.LastRecordTime}
	var names []string
	for _, id := range j.AccountIDs() {
		for _, a := range accounts {
			if a.ID == id {
				names = append(names, accountWords(accounts, a.Type, a.Name))
			}
		}
	}
	v.Account = strings.Join(names, "、")
	return v
}

// coversSite says whether a website backup task includes the site.
func coversSite(j onepanel.Cronjob, id uint) bool {
	if j.Type != "website" {
		return false
	}
	if j.Website == "all" {
		return true
	}
	for _, s := range strings.Split(j.Website, ",") {
		if n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64); err == nil && uint(n) == id {
			return true
		}
	}
	return false
}

// SiteBackups reads a site's backups and backup schedules.
func (a *App) SiteBackups(ctx context.Context, serverID int64, siteID uint) (SiteBackupsView, error) {
	if a.isBT(serverID) {
		return a.btSiteBackups(ctx, serverID, siteID)
	}
	var v SiteBackupsView
	err := a.withPanel(ctx, serverID, func(_ store.Server, _ sshx.Conn, p *onepanel.Client) error {
		d, err := p.Website(ctx, siteID)
		if err != nil {
			return userErr("读取网站失败：%v", err)
		}
		if v.Accounts, err = p.BackupAccounts(ctx); err != nil {
			return userErr("读取 1Panel 的备份账号失败：%v", err)
		}
		list, err := p.BackupList(ctx, "website", d.Alias, d.Alias)
		if err != nil {
			return userErr("读取网站的备份失败：%v", err)
		}
		v.Backups = []BackupView{}
		for _, b := range list {
			v.Backups = append(v.Backups, BackupView{ID: b.ID, File: b.FileName, CreatedAt: b.CreatedAt,
				Account: accountWords(v.Accounts, b.AccountType, b.AccountName), Local: strings.EqualFold(b.AccountType, "LOCAL"),
				Status: b.Status, Message: b.Message, Note: b.Description,
				// 1Panel's scheduled tasks leave no note; Miao Panel's backups always have one.
				Automatic: b.Description == "", BeforeWrite: strings.HasPrefix(b.Description, "Miao Panel 修改前") || strings.HasPrefix(b.Description, "Miao Panel 恢复前")})
		}
		jobs, err := p.Cronjobs(ctx, "")
		if err != nil {
			return userErr("读取 1Panel 的计划任务失败：%v", err)
		}
		v.Others = []BackupScheduleView{}
		mine := actions.BackupJobName(d.PrimaryDomain)
		for _, j := range jobs {
			switch {
			case j.Name == mine:
				s := scheduleOf(j, v.Accounts)
				v.Schedule = &s
			case coversSite(j, siteID):
				v.Others = append(v.Others, scheduleOf(j, v.Accounts))
			}
		}
		return nil
	})
	return v, err
}

// BackupPath makes a backup's file available on the server (fetching it
// from a remote account first) and returns where it is, for downloading.
func (a *App) BackupPath(ctx context.Context, serverID int64, siteID, backupID uint) (string, error) {
	if a.isBT(serverID) {
		return a.btBackupPath(ctx, serverID, siteID, backupID)
	}
	var path string
	err := a.withPanel(ctx, serverID, func(_ store.Server, _ sshx.Conn, p *onepanel.Client) error {
		d, err := p.Website(ctx, siteID)
		if err != nil {
			return userErr("读取网站失败：%v", err)
		}
		list, err := p.BackupList(ctx, "website", d.Alias, d.Alias)
		if err != nil {
			return userErr("读取网站的备份失败：%v", err)
		}
		for _, b := range list {
			if b.ID == backupID {
				if path, err = p.FetchBackup(ctx, b); err != nil {
					return userErr("取备份文件失败：%v", err)
				}
				if !strings.HasPrefix(path, "/") {
					return userErr("1Panel 给的备份路径不对：%s", path)
				}
				return nil
			}
		}
		return userErr("找不到这份备份，可能已经被删除了")
	})
	return path, err
}

// toolPanelBackups is the AI's look at a site's backups.
func (a *App) toolPanelBackups(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		ServerID int64  `json:"server_id"`
		Website  string `json:"website"`
	}
	if err := parseArgs(raw, &in); err != nil {
		return "", err
	}
	if msg := a.noPanel(in.ServerID, true); msg != "" {
		return msg, nil
	}
	var id uint
	panel := "1Panel"
	var err error
	if a.isBT(in.ServerID) {
		panel = "宝塔"
		err = a.withBT(ctx, in.ServerID, func(_ store.Server, _ sshx.Conn, b *btpanel.Client) error {
			s, err := b.Site(ctx, strings.TrimSpace(in.Website))
			id = uint(s.ID)
			return err
		})
	} else {
		err = a.withPanel(ctx, in.ServerID, func(_ store.Server, _ sshx.Conn, p *onepanel.Client) error {
			s, err := findPanelSite(ctx, p, strings.TrimSpace(in.Website))
			id = s.ID
			return err
		})
	}
	if err != nil {
		return "", err
	}
	v, err := a.SiteBackups(ctx, in.ServerID, id)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "网站 %s 的备份（%s）：\n", in.Website, panel)
	if panel == "宝塔" {
		b.WriteString("宝塔的定时备份在宝塔面板「计划任务」里设置，Miao Panel 不能设置；宝塔的网站备份也只能在宝塔面板里恢复。\n")
	}
	if v.Schedule != nil {
		fmt.Fprintf(&b, "定时备份（Miao Panel 设置）：%s，保留 %d 份，放在 %s", orText(v.Schedule.Time, v.Schedule.Spec), v.Schedule.Keep, v.Schedule.Account)
		if v.Schedule.LastAt != "" {
			fmt.Fprintf(&b, "，上次 %s %s", v.Schedule.LastAt, v.Schedule.LastStatus)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("没有 Miao Panel 设置的定时备份（可以用 site.backup.schedule 设置）\n")
	}
	for _, o := range v.Others {
		fmt.Fprintf(&b, "1Panel 计划任务「%s」也备份这个网站：%s，保留 %d 份\n", o.Name, orText(o.Time, o.Spec), o.Keep)
	}
	if len(v.Backups) == 0 {
		b.WriteString("还没有备份\n")
	}
	for i, x := range v.Backups {
		if i == 20 {
			fmt.Fprintf(&b, "……还有 %d 份更早的\n", len(v.Backups)-20)
			break
		}
		fmt.Fprintf(&b, "- file=%s 时间=%s 位置=%s 状态=%s", x.File, x.CreatedAt, x.Account, x.Status)
		if x.Note != "" {
			fmt.Fprintf(&b, " 备注=%s", x.Note)
		}
		b.WriteString("\n")
	}
	var acc []string
	for _, x := range v.Accounts {
		acc = append(acc, x.Name+"（"+x.Type+"）")
	}
	fmt.Fprintf(&b, "可用的备份账号：%s\n", strings.Join(acc, "、"))
	return b.String(), nil
}

func orText(a, b string) string {
	if a != "" {
		return "每天 " + a
	}
	return "cron " + b
}
