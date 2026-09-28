package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
)

// ---- Backups of 1Panel websites ----
//
// A backup is 1Panel's own: the site's folder and config packed into its
// backup folder, or into a backup account (COS and the like) set up in
// 1Panel. A schedule is one of 1Panel's cron jobs, so it runs on the
// server even when Miao Panel is closed. Restoring puts a backup back
// after taking one of the site as it is now.

// BackupJobPrefix starts the name of every scheduled backup Miao Panel
// sets up in 1Panel.
const BackupJobPrefix = "Miao Panel 备份 "

// BackupJobName is the name of a site's scheduled backup.
func BackupJobName(domain string) string { return BackupJobPrefix + strings.ToLower(domain) }

var clockRe = regexp.MustCompile(`^([01]?[0-9]|2[0-3]):([0-5][0-9])$`)

// BackupJob finds the scheduled backup Miao Panel set up for a site.
func BackupJob(ctx context.Context, c *onepanel.Client, domain string) (onepanel.Cronjob, bool, error) {
	name := BackupJobName(domain)
	jobs, err := c.Cronjobs(ctx, name)
	if err != nil {
		return onepanel.Cronjob{}, false, err
	}
	for _, j := range jobs {
		if j.Name == name {
			return j, true, nil
		}
	}
	return onepanel.Cronjob{}, false, nil
}

// BackupClock reads the time of day a daily job runs at, as "03:00".
func BackupClock(spec string) string {
	f := strings.Fields(spec)
	if len(f) != 5 || f[2] != "*" || f[3] != "*" || f[4] != "*" {
		return ""
	}
	m, err1 := strconv.Atoi(f[0])
	h, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", h, m)
}

// backupAccount finds a 1Panel backup account by name or type; empty is
// the server's own backup folder.
func backupAccount(ctx context.Context, c *onepanel.Client, want string) (onepanel.BackupAccount, error) {
	list, err := c.BackupAccounts(ctx)
	if err != nil {
		return onepanel.BackupAccount{}, fmt.Errorf("读取 1Panel 的备份账号失败：%w", err)
	}
	if want == "" {
		want = "LOCAL"
	}
	for _, a := range list {
		if a.Name == want {
			return a, nil
		}
	}
	for _, a := range list {
		if strings.EqualFold(a.Type, want) {
			return a, nil
		}
	}
	var names []string
	for _, a := range list {
		names = append(names, a.Name)
	}
	return onepanel.BackupAccount{}, fmt.Errorf("1Panel 里没有备份账号 %s（有的是：%s）", want, strings.Join(names, "、"))
}

// checkBackup makes sure a backup 1Panel calls done is a file with
// something in it, before anything is deleted or overwritten on its word.
func checkBackup(ctx context.Context, env *Env, kind, name, detail string, rec onepanel.BackupRecord) (int64, error) {
	if strings.TrimSpace(rec.FileDir) == "" || strings.TrimSpace(rec.FileName) == "" {
		return 0, errors.New("1Panel 报告备份成功，但没有返回备份文件位置；请在 1Panel「备份」里核对")
	}
	var size int64
	var err error
	for attempt := 0; ; attempt++ {
		if size, err = env.OnePanel.BackupSize(ctx, kind, name, detail, rec.ID); err == nil && size > 0 {
			return size, nil
		}
		if attempt == 2 {
			return 0, fmt.Errorf("1Panel 报告备份成功，但无法确认文件有内容（%s/%s）：大小 %d，错误 %v；请在 1Panel「备份」里核对", rec.FileDir, rec.FileName, size, err)
		}
		select {
		case <-ctx.Done():
			return 0, errors.New("核对备份文件大小时连接中断")
		case <-time.After(pollEvery(env)):
		}
	}
}

// waitBackup waits until the backup started with task is written, and
// checks the file is there.
func waitBackup(ctx context.Context, env *Env, kind, name, detail, task string) (onepanel.BackupRecord, error) {
	deadline := time.Now().Add(backupTimeout)
	for {
		rec, found, err := env.OnePanel.FindBackup(ctx, kind, name, detail, task)
		if err == nil && found && rec.Status == "Success" {
			_, err := checkBackup(ctx, env, kind, name, detail, rec)
			return rec, err
		}
		if err == nil && found && rec.Status == "Failed" {
			return rec, errors.New(rec.Message)
		}
		if time.Now().After(deadline) {
			return rec, fmt.Errorf("等了 %d 分钟还没完成", int(backupTimeout.Minutes()))
		}
		select {
		case <-ctx.Done():
			return rec, errors.New("等待时超时了")
		case <-time.After(pollEvery(env)):
		}
	}
}

// waitTask waits until a 1Panel background task ends.
func waitTask(ctx context.Context, env *Env, task string) error {
	deadline := time.Now().Add(backupTimeout)
	for {
		t, found, err := env.OnePanel.Task(ctx, task)
		if err == nil && found && t.Status == "Success" {
			return nil
		}
		if err == nil && found && t.Status == "Failed" {
			return errors.New(t.ErrorMsg)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等了 %d 分钟还没完成，请在 1Panel「任务」里查看", int(backupTimeout.Minutes()))
		}
		select {
		case <-ctx.Done():
			return errors.New("等待时超时了")
		case <-time.After(pollEvery(env)):
		}
	}
}

// ---- back up now ----

func applySiteBackup(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	if site.Alias == "" {
		return refused(out, "读不到网站 %s 的目录名，不能备份", site.PrimaryDomain)
	}
	note := v["note"]
	if note == "" {
		note = "Miao Panel 手动备份"
	}
	task := newTaskID()
	report("正在备份网站 %s（网站目录和配置）……", site.PrimaryDomain)
	if err := c.BackupNote(ctx, "website", site.Alias, site.Alias, task, note); err != nil {
		out.Status = StatusFailed
		out.logf("备份失败：%v", err)
		return *out
	}
	rec, err := waitBackup(ctx, env, "website", site.Alias, site.Alias, task)
	if err != nil {
		out.Status = StatusFailed
		out.logf("备份失败：%v", err)
		return *out
	}
	report("完成：已备份到 %s/%s", rec.FileDir, rec.FileName)
	out.Result = map[string]string{"backup": rec.FileName}
	out.Status, out.Undo = StatusDone, map[string]string{}
	return *out
}

// ---- scheduled backups ----

func applyBackupSchedule(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	clock := v["time"]
	if clock == "" {
		clock = "03:00"
	}
	m := clockRe.FindStringSubmatch(clock)
	if m == nil {
		return refused(out, "时间要写成 03:00 这样，%q 不行", clock)
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	keep, _ := strconv.Atoi(v["keep"])
	if keep == 0 {
		keep = 7
	}
	acc, err := backupAccount(ctx, c, v["account"])
	if err != nil {
		return refused(out, "%v", err)
	}
	old, found, err := BackupJob(ctx, c, site.PrimaryDomain)
	if err != nil {
		return refused(out, "读取 1Panel 的计划任务失败：%v", err)
	}
	job := onepanel.Cronjob{
		Name: BackupJobName(site.PrimaryDomain), Type: "website", Spec: fmt.Sprintf("%d %d * * *", mi, h),
		Website: strconv.FormatUint(uint64(site.ID), 10), RetainCopies: keep, RetryTimes: 1, Timeout: 3600,
		SourceAccountIDs: strconv.FormatUint(uint64(acc.ID), 10), DownloadAccountID: acc.ID,
	}
	where := "服务器上的 1Panel 备份目录"
	if !strings.EqualFold(acc.Type, "LOCAL") {
		where = "备份账号 " + acc.Name
	}
	if found {
		if old.Spec == job.Spec && old.Website == job.Website && old.RetainCopies == keep &&
			old.SourceAccountIDs == job.SourceAccountIDs && old.DownloadAccountID == acc.ID && old.Status != "Disable" {
			report("网站 %s 已经是每天 %s 备份、保留 %d 份，不需要修改", site.PrimaryDomain, clock, keep)
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
		job.ID = old.ID
		report("正在修改网站 %s 的定时备份：每天 %s，保留最近 %d 份，放在%s", site.PrimaryDomain, clock, keep, where)
	} else {
		report("正在给网站 %s 设置定时备份：每天 %s，保留最近 %d 份，放在%s", site.PrimaryDomain, clock, keep, where)
	}
	if err := c.SaveCronjob(ctx, job); err != nil {
		return refused(out, "设置失败：%v", err)
	}
	out.remember(site)
	if found {
		b, _ := json.Marshal(old)
		out.Undo["job"] = string(b)
		if old.Status == "Disable" {
			// Switched off in 1Panel: a daily backup that never runs is none.
			if err := c.SetCronjobStatus(ctx, old.ID, "Enable"); err != nil {
				out.Status = StatusDone
				report("设置已保存，但这个计划任务在 1Panel 里是停用的，启用失败：%v。请在 1Panel「计划任务」里启用它", err)
				return *out
			}
			report("这个计划任务原来在 1Panel 里停用了，已重新启用")
		}
	} else {
		out.Undo["created"] = "true"
	}
	report("完成：定时备份由 1Panel 在服务器上运行，Miao Panel 关着也会备份。到时间后可以在网站的「备份」里看到")
	out.Status = StatusDone
	return *out
}

func undoBackupSchedule(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	if undo["created"] == "true" {
		j, found, err := BackupJob(ctx, c, undo["domain"])
		if err != nil || !found {
			return err
		}
		return c.DeleteCronjob(ctx, j.ID)
	}
	if _, err := sameSite(ctx, c, undo); err != nil {
		return err
	}
	var old onepanel.Cronjob
	if err := json.Unmarshal([]byte(undo["job"]), &old); err != nil {
		return fmt.Errorf("读不到原来的设置：%w", err)
	}
	cur, found, err := BackupJob(ctx, c, undo["domain"])
	if err != nil {
		return err
	}
	status := old.Status
	old.ID, old.Status = 0, ""
	if found {
		old.ID = cur.ID
	}
	if err := c.SaveCronjob(ctx, old); err != nil {
		return err
	}
	if status == "Disable" && found {
		return c.SetCronjobStatus(ctx, cur.ID, "Disable")
	}
	return nil
}

func applyBackupUnschedule(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	job, found, err := BackupJob(ctx, c, site.PrimaryDomain)
	if err != nil {
		return refused(out, "读取 1Panel 的计划任务失败：%v", err)
	}
	if !found {
		report("网站 %s 没有 Miao Panel 设置的定时备份，不需要取消", site.PrimaryDomain)
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	report("正在取消网站 %s 的定时备份（已有的备份文件保留）", site.PrimaryDomain)
	if err := c.DeleteCronjob(ctx, job.ID); err != nil {
		return refused(out, "取消失败：%v", err)
	}
	out.remember(site)
	b, _ := json.Marshal(job)
	out.Undo["job"] = string(b)
	report("完成：以后不再自动备份，已有的备份还在")
	out.Status = StatusDone
	return *out
}

func undoBackupUnschedule(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	if _, err := sameSite(ctx, c, undo); err != nil {
		return err
	}
	var job onepanel.Cronjob
	if err := json.Unmarshal([]byte(undo["job"]), &job); err != nil {
		return fmt.Errorf("读不到原来的设置：%w", err)
	}
	if _, found, err := BackupJob(ctx, c, undo["domain"]); err != nil || found {
		return err
	}
	job.ID, job.Status, job.LastRecordStatus, job.LastRecordTime = 0, "", "", ""
	return c.SaveCronjob(ctx, job)
}

// ---- restoring ----

func applySiteRestore(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	if site.Alias == "" {
		return refused(out, "读不到网站 %s 的目录名，没有恢复", site.PrimaryDomain)
	}
	list, err := c.BackupList(ctx, "website", site.Alias, site.Alias)
	if err != nil {
		return refused(out, "读取网站的备份失败：%v", err)
	}
	var from onepanel.BackupInfo
	for _, b := range list {
		if b.FileName == v["backup"] {
			from = b
			break
		}
	}
	if from.ID == 0 {
		return refused(out, "网站 %s 没有叫 %s 的备份", site.PrimaryDomain, v["backup"])
	}
	if from.Status != "" && from.Status != "Success" {
		return refused(out, "备份 %s 没有成功完成，不能用来恢复", from.FileName)
	}

	task := newTaskID()
	report("先备份网站 %s 现在的样子……", site.PrimaryDomain)
	if err := c.BackupNote(ctx, "website", site.Alias, site.Alias, task, "Miao Panel 恢复前备份"); err != nil {
		return refused(out, "备份失败，没有恢复：%v", err)
	}
	now, err := waitBackup(ctx, env, "website", site.Alias, site.Alias, task)
	if err != nil {
		return refused(out, "备份失败，没有恢复：%v", err)
	}
	report("已备份现在的网站：%s", now.FileName)
	out.Result = map[string]string{"before": now.FileName}

	report("正在取出备份 %s……", from.FileName)
	file, err := c.FetchBackup(ctx, from)
	if err != nil {
		return refused(out, "取不到备份文件，没有恢复：%v", err)
	}
	task = newTaskID()
	report("正在恢复网站 %s（1Panel 会先把现在的网站另存一份，恢复失败时自动退回）……", site.PrimaryDomain)
	if err := c.Restore(ctx, "website", site.Alias, site.Alias, from, file, task); err != nil {
		out.Status = StatusFailed
		out.logf("恢复失败：%v。网站没有改动；恢复前的备份是 %s", err, now.FileName)
		return *out
	}
	if err := waitTask(ctx, env, task); err != nil {
		out.Status = StatusFailed
		if ctx.Err() != nil {
			out.logf("恢复还在 1Panel 里进行，Miao Panel 等不及了：请在 1Panel「任务」里看结果，不要再点一次恢复。恢复前的备份是 %s", now.FileName)
		} else {
			out.logf("恢复失败：%v。1Panel 会退回到恢复前的样子；另外还有恢复前的备份 %s", err, now.FileName)
		}
		return *out
	}
	report("完成：网站 %s 已恢复到 %s。想退回去，可以用恢复前的备份 %s 再恢复一次", site.PrimaryDomain, backupWhen(from), now.FileName)
	out.Status, out.Undo = StatusDone, map[string]string{}
	return *out
}

// backupWhen says when a backup was made, in words.
func backupWhen(b onepanel.BackupInfo) string {
	if t, err := time.Parse(time.RFC3339, b.CreatedAt); err == nil {
		return t.Local().Format("1 月 2 日 15:04") + " 的备份"
	}
	return "备份 " + b.FileName
}

func init() {
	site := Param{Name: "website", Kind: "host", Required: true, Desc: "1Panel 网站的主域名（panel_websites 返回的域名）"}
	panel := func(op, downtime, undo string) map[string]Impl {
		return map[string]Impl{"1panel": {Via: "1Panel 接口", Panel: op, Downtime: downtime, Undo: undo}}
	}
	register(&Capability{
		Name: "site.backup", Title: "备份网站", Risk: core.R1,
		NoUndo: "备份只是多出一个备份文件，不改动网站；不需要时可以在网站的「备份」里删除",
		Params: []Param{site, {Name: "note", Kind: "text", Desc: "备注，例如「改版前」；不填是「Miao Panel 手动备份」"}},
		Impls:  panel("site_backup", "不影响访问；网站很大时服务器会忙一会儿", ""),
	})
	register(&Capability{
		Name: "site.backup.schedule", Title: "设置网站定时备份", Risk: core.R1, Reversible: true,
		Params: []Param{site,
			{Name: "time", Kind: "text", Default: "03:00", Desc: "每天几点备份，例如 03:00（服务器的时间）"},
			{Name: "keep", Kind: "int", Min: 1, Max: 365, Default: "7", Desc: "保留最近几份，更早的自动删除"},
			{Name: "account", Kind: "text", Desc: "放在哪个 1Panel 备份账号（名称或类型，例如 COS）；不填放在服务器上的 1Panel 备份目录"}},
		Impls: panel("backup_schedule", "不影响访问；由 1Panel 的计划任务在服务器上运行", "新建的定时备份删除；修改的恢复原来的时间和份数"),
		Check: func(v map[string]string) error {
			if t := v["time"]; t != "" && !clockRe.MatchString(t) {
				return fmt.Errorf("time 要写成 03:00 这样，%q 不行", t)
			}
			return nil
		},
	})
	register(&Capability{
		Name: "site.backup.unschedule", Title: "取消网站定时备份", Risk: core.R2, Reversible: true,
		Params: []Param{site},
		Impls:  panel("backup_unschedule", "不影响访问；以后不再自动备份，已有的备份保留", "按原来的时间和份数重新设置定时备份"),
	})
	register(&Capability{
		Name: "site.restore", Title: "从备份恢复网站", Risk: core.R3,
		NoUndo: "恢复前会先把网站现在的样子备份一份，需要时可以用那份备份再恢复回来；1Panel 恢复失败时也会自动退回",
		Params: []Param{site, {Name: "backup", Kind: "text", Required: true, Desc: "备份的文件名（panel_backups 返回的 file）"}},
		Impls:  panel("site_restore", "恢复期间网站可能短暂打不开；网站目录和配置换成备份里的", ""),
	})
}
