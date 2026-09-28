package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/update"
)

// Keeping Miao Panel up to date: a daily look at GitHub for a newer
// release (can be turned off), shown in 设置 and on 总览, and, where the
// program can replace itself, an update in one click.

const updateCheckKey = "update_check" // "off" turns the daily look off

type updateState struct {
	mu        sync.Mutex
	latest    *update.Release
	checkedAt time.Time
	err       string
	applying  bool
}

// UpdateView is the 设置 page's 版本 section.
type UpdateView struct {
	Current   string          `json:"current"`
	Latest    *update.Release `json:"latest,omitempty"`
	Newer     bool            `json:"newer"`
	CheckedAt string          `json:"checkedAt,omitempty"`
	Error     string          `json:"error"`
	Enabled   bool            `json:"enabled"`  // the daily look
	CanApply  bool            `json:"canApply"` // one click can update here
	Why       string          `json:"why"`
	OS        string          `json:"os"`
	Applying  bool            `json:"applying"`
}

func (a *App) updateEnabled() bool {
	v, _ := a.Store.Setting(updateCheckKey)
	return v != "off"
}

// UpdateStatus says what is known about updates now.
func (a *App) UpdateStatus() UpdateView {
	v := UpdateView{Current: a.Version, Enabled: a.updateEnabled(), OS: runtime.GOOS + "/" + runtime.GOARCH}
	exe, exeErr := a.updateExe()
	switch {
	case update.InDocker():
		v.Why = "Docker 版在服务器的项目目录里运行 git pull && docker compose up -d --build 更新，数据在卷里不会丢"
	case !update.IsRelease(a.Version):
		v.Why = "这是自己编译的开发版，不能自动更新"
	case a.Restart == nil || exeErr != nil:
		v.Why = "这种运行方式不能自动更新，请下载新版本替换"
	case !update.Writable(filepath.Dir(exe)):
		v.Why = "Miao Panel 不能写入程序所在的目录 " + filepath.Dir(exe) + "，请按部署文档的升级步骤下载新版本替换（systemd 版用 sudo install）"
	default:
		v.CanApply = true
	}
	a.upd.mu.Lock()
	defer a.upd.mu.Unlock()
	v.Latest, v.Error, v.Applying = a.upd.latest, a.upd.err, a.upd.applying
	if !a.upd.checkedAt.IsZero() {
		v.CheckedAt = a.upd.checkedAt.UTC().Format(time.RFC3339)
	}
	if v.Latest != nil {
		v.Newer = update.Newer(v.Latest.Version, a.Version)
	}
	return v
}

// updateExe is the program an update replaces.
func (a *App) updateExe() (string, error) {
	if a.UpdateExe != "" {
		return a.UpdateExe, nil
	}
	return update.Executable()
}

// CheckUpdate asks GitHub for the newest release now.
func (a *App) CheckUpdate(ctx context.Context) (UpdateView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rel, err := a.updater().Latest(ctx)
	a.upd.mu.Lock()
	a.upd.checkedAt = time.Now()
	if err != nil {
		a.upd.err = err.Error()
	} else {
		a.upd.latest, a.upd.err = &rel, ""
	}
	a.upd.mu.Unlock()
	if err != nil {
		return a.UpdateStatus(), userErr("检查更新失败：%v", err)
	}
	return a.UpdateStatus(), nil
}

func (a *App) updater() *update.Checker {
	if a.Updater != nil {
		return a.Updater
	}
	return &update.Checker{}
}

// SetUpdateCheck turns the daily look for a new version on or off.
func (a *App) SetUpdateCheck(on bool) (UpdateView, error) {
	v := "on"
	if !on {
		v = "off"
	}
	if err := a.Store.SetSetting(updateCheckKey, v); err != nil {
		return UpdateView{}, err
	}
	return a.UpdateStatus(), nil
}

// ApplyUpdate starts updating to the newest release. In the background
// it downloads this system's program, checks it, puts it in place and
// restarts into it; the page follows along through UpdateStatus. No
// checklist may be running, and none starts until then.
func (a *App) ApplyUpdate(ctx context.Context) (UpdateView, error) {
	v := a.UpdateStatus()
	if !v.CanApply {
		return v, userErr("%s", v.Why)
	}
	if v.Latest == nil || !v.Newer {
		var err error
		if v, err = a.CheckUpdate(ctx); err != nil {
			return v, err
		}
		if !v.Newer {
			return v, userErr("已经是最新版本了")
		}
	}
	exe, err := a.updateExe()
	if err != nil {
		return v, userErr("找不到程序文件：%v", err)
	}
	a.upd.mu.Lock()
	if a.upd.applying {
		a.upd.mu.Unlock()
		return v, userErr("正在更新，请稍等")
	}
	if !a.locks.close() {
		a.upd.mu.Unlock()
		return v, userErr("有清单正在执行，等它完成再更新")
	}
	a.upd.applying, a.upd.err = true, ""
	a.upd.mu.Unlock()
	go a.runUpdate(*v.Latest, exe)
	return a.UpdateStatus(), nil
}

func (a *App) runUpdate(rel update.Release, exe string) {
	fail := func(msg string) {
		a.locks.reopen()
		a.upd.mu.Lock()
		a.upd.applying, a.upd.err = false, msg
		a.upd.mu.Unlock()
	}
	// However slow the line to GitHub: the program is some 30 MB.
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	next, err := a.updater().Download(ctx, rel, runtime.GOOS, runtime.GOARCH, filepath.Dir(exe))
	if err != nil {
		fail(err.Error())
		return
	}
	if err := update.Install(exe, next); err != nil {
		_ = os.Remove(next)
		fail(err.Error())
		return
	}
	_ = a.Store.Audit("user", "update", a.Version+" → "+rel.Version, "")
	time.Sleep(1500 * time.Millisecond) // the page hears it is ready first
	if err := a.Restart(exe); err != nil {
		fail("新版本已经装好，但没能自动重启：" + err.Error() + "。请手动重新打开 Miao Panel")
	}
}

// UpdateLoop looks for a new version a minute after starting and then
// once a day, when the look is on.
func (a *App) UpdateLoop(ctx context.Context) {
	if !update.IsRelease(a.Version) {
		return
	}
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = 24 * time.Hour
		if a.updateEnabled() {
			_, _ = a.CheckUpdate(ctx)
		}
	}
}

// updateTodo is 总览's line about a new version, if there is one.
func (a *App) updateTodo() *OverviewItem {
	v := a.UpdateStatus()
	if !v.Newer || v.Latest == nil {
		return nil
	}
	// The first point of the release notes, past its headings.
	note := "现在用的是 " + v.Current
	for _, l := range strings.Split(v.Latest.Notes, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		note = strings.TrimSpace(strings.TrimLeft(l, "-*• "))
		break
	}
	return &OverviewItem{Level: "info", Kind: "update", Title: "有新版本 " + v.Latest.Version, Meta: firstRunes(note, 60), Action: "查看"}
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
