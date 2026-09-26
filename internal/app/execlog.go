package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/scripts"
)

// Who started something that runs on a server.
const (
	OriginUser = "user" // the user clicked a button
	OriginAI   = "ai"   // the AI ran a read-only check while answering
	OriginPlan = "plan" // a checklist the user approved
	OriginAuto = "auto" // Miao Panel keeping statistics up to date in the background
)

type originKey struct{}

func withOrigin(ctx context.Context, origin string) context.Context {
	return context.WithValue(ctx, originKey{}, origin)
}

func originOf(ctx context.Context) string {
	if o, ok := ctx.Value(originKey{}).(string); ok {
		return o
	}
	return OriginUser
}

const maxLogOutput = 64 << 10

func clip(s string) string {
	if len(s) <= maxLogOutput {
		return s
	}
	cut := maxLogOutput
	for cut > 0 && s[cut]&0xC0 == 0x80 { // do not split a UTF-8 character
		cut--
	}
	return s[:cut] + "\n（输出过长，只保留了前 64KB）"
}

// startExec records that something is about to run on a server. Logging
// problems never stop the work itself; the entry then has no ID.
func (a *App) startExec(e store.ExecLog) store.ExecLog {
	e.Status = store.ExecRunning
	if saved, err := a.Store.AddExec(e); err == nil {
		return saved
	}
	return e
}

func (a *App) finishExec(e *store.ExecLog, status, output string) {
	e.Status, e.Output, e.FinishedAt = status, clip(output), now()
	if e.ID > 0 {
		_ = a.Store.UpdateExec(*e)
	}
}

func (a *App) finishAction(e *store.ExecLog, out actions.Outcome) {
	e.Commands = strings.Join(out.Commands, "\n")
	e.ScriptName, e.Script = out.ScriptName, out.Script
	e.Undo, e.BackupDir, e.RollbackFile = out.Undo, out.BackupDir, out.RollbackFile
	a.finishExec(e, out.Status, strings.Join(out.Log, "\n"))
}

// paramText shows a step's parameters compactly, e.g. "size_gb=2".
func paramText(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+values[k])
	}
	return strings.Join(parts, "，")
}

// ExecView is a log entry with what a rollback would do, or why there is
// nothing to roll back.
type ExecView struct {
	store.ExecLog
	CanRollback bool   `json:"canRollback"`
	RollbackHow string `json:"rollbackHow,omitempty"`
	NoRollback  string `json:"noRollback,omitempty"`
}

func execView(e store.ExecLog) ExecView {
	v := ExecView{ExecLog: e}
	switch {
	case e.Kind == store.ExecRead:
		v.NoRollback = "只读操作，没有修改服务器，不需要回滚"
	case e.Kind == store.ExecRollback:
		v.NoRollback = "这是一次回滚操作"
		if e.UndoOf > 0 {
			v.NoRollback += fmt.Sprintf("（撤销的是记录 #%d）", e.UndoOf)
		}
	case e.UndoneBy > 0:
		v.NoRollback = fmt.Sprintf("已经回滚过了（见记录 #%d）", e.UndoneBy)
	case e.Status == store.ExecRunning:
		v.NoRollback = "还在执行中"
	case e.Status == store.ExecInterrupted:
		v.NoRollback = "Miao Panel 在执行过程中被关闭了，结果未知。请重新识别服务器，确认改动有没有生效"
	case e.Status == actions.StatusRefused:
		v.NoRollback = "条件不满足，没有做任何修改，不需要回滚"
	case e.Status == actions.StatusRolledBack:
		v.NoRollback = "执行失败后已经自动恢复原状，不需要回滚"
	case e.Status == actions.StatusFailed:
		v.NoRollback = "执行失败，而且没能完全自动恢复，需要人工检查"
		if e.BackupDir != "" {
			v.NoRollback += "。修改前的文件备份在服务器的 " + e.BackupDir
		}
	case e.Status == actions.StatusDone:
		r, err := actions.Resolve(e.Capability, e.Params, e.Adapter)
		switch {
		case err != nil:
			v.NoRollback = "当前版本的 Miao Panel 不支持回滚这个操作"
		case !r.Cap.Reversible:
			v.NoRollback = r.Cap.NoUndo
		case len(e.Undo) == 0 && e.ID > 0:
			v.NoRollback = "这一步当时没有做任何修改，不需要回滚"
		default:
			v.CanRollback, v.RollbackHow = true, r.Impl.Undo
		}
	}
	return v
}

// ExecLogs lists what Miao Panel ran on servers, newest first.
func (a *App) ExecLogs(changesOnly bool) ([]ExecView, error) {
	list, err := a.Store.ListExec(changesOnly, 500)
	if err != nil {
		return nil, err
	}
	out := make([]ExecView, 0, len(list))
	for _, e := range list {
		out = append(out, execView(e))
	}
	return out, nil
}

// ExecEntry returns one log entry with its commands, script and output.
func (a *App) ExecEntry(id int64) (ExecView, error) {
	e, err := a.Store.GetExec(id)
	if err != nil {
		return ExecView{}, userErr("找不到这条记录（编号 %d）", id)
	}
	if e.Script == "" && e.ScriptName == "discover.sh" {
		e.Script = scripts.Discover
	}
	return execView(e), nil
}

// Rollback reverts the change a log entry records.
func (a *App) Rollback(ctx context.Context, id int64) (ExecView, error) {
	e, err := a.Store.GetExec(id)
	if err != nil {
		return ExecView{}, userErr("找不到这条记录（编号 %d）", id)
	}
	if v := execView(e); !v.CanRollback {
		return v, userErr("这条记录不能回滚：%s", v.NoRollback)
	}
	r, _ := actions.Resolve(e.Capability, e.Params, e.Adapter)
	if _, err := a.planServer(e.ServerID); err != nil && r.Impl.NeedsServer() {
		msg := "这台服务器已经从 Miao Panel 里删除了，无法回滚"
		if e.RollbackFile != "" {
			msg += "。可以在服务器上用 root 执行 sh " + e.RollbackFile + " 回滚"
		}
		return execView(e), userErr("%s", msg)
	}
	if !a.locks.try(e.ServerID) {
		return execView(e), userErr("这台服务器上正在执行其他操作，请稍后再试")
	}
	defer a.locks.release(e.ServerID)
	// Read again under the lock: the same rollback may have just run.
	if e, err = a.Store.GetExec(id); err != nil {
		return ExecView{}, err
	}
	if v := execView(e); !v.CanRollback {
		return v, userErr("这条记录不能回滚：%s", v.NoRollback)
	}
	_, rerr := a.rollback(ctx, &e)
	v, err := a.ExecEntry(id)
	if rerr != nil {
		return v, rerr
	}
	return v, err
}

// rollback reverts one change and keeps its checklist in step. The caller
// holds the server lock. e.ID is 0 for steps run before the execution log
// existed.
func (a *App) rollback(ctx context.Context, e *store.ExecLog) (store.ExecLog, error) {
	r, err := actions.Resolve(e.Capability, e.Params, e.Adapter)
	if err != nil {
		return store.ExecLog{}, userErr("无法回滚：%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	sv := store.Server{ID: e.ServerID, Name: e.ServerName}
	env := &actions.Env{Cloud: a.tencentClient(), PollInterval: a.PollInterval}
	if r.Impl.NeedsServer() {
		if sv, env, err = a.env(ctx, e.ServerID); err != nil {
			return store.ExecLog{}, friendlySSHError(err)
		}
		defer func() { env.SSH.Close() }()
	}

	rb := a.startExec(store.ExecLog{
		ServerID: sv.ID, ServerName: sv.Name, Adapter: e.Adapter, Origin: OriginUser, Kind: store.ExecRollback,
		Title: "回滚：" + e.Title, Capability: e.Capability, Params: e.Params, Via: e.Via,
		PlanID: e.PlanID, StepIdx: e.StepIdx, UndoOf: e.ID,
	})
	out := actions.Undo(ctx, env, r, e.Undo)
	if out.Status == actions.StatusUndone && e.RollbackFile != "" {
		cmd := actions.RetireRollbackFile(ctx, env, e.RollbackFile)
		out.Commands = append(out.Commands, "# 把服务器上的回滚文件改名，避免被再次执行", cmd)
	}
	a.finishAction(&rb, out)
	if out.Status == actions.StatusUndone && e.ID > 0 {
		e.UndoneBy = rb.ID
		_ = a.Store.UpdateExec(*e)
	}
	if e.PlanID > 0 {
		if v, err := a.Plan(e.PlanID); err == nil && e.StepIdx >= 0 && e.StepIdx < len(v.StepList) && v.StepList[e.StepIdx].LogID == e.ID {
			st := &v.StepList[e.StepIdx]
			st.Log = append(st.Log, out.Log...)
			if out.Status == actions.StatusUndone {
				st.Status = actions.StatusUndone
			}
			if env.SSH != nil {
				if prof, err := a.discoverWith(withOrigin(ctx, OriginUser), sv, env.SSH, "回滚后重新识别"); err == nil {
					v.After = snapshotOf(prof)
				}
			}
			_ = a.savePlan(&v)
		}
	}
	a.forgetCertificates()
	_ = a.Store.Audit("user", "exec.rollback", e.Title, fmt.Sprintf("%s：%s", sv.Name, out.Status))
	if out.Status != actions.StatusUndone {
		return rb, userErr("回滚没有成功：%s", strings.Join(out.Log, "；"))
	}
	return rb, nil
}

// recoverInterrupted cleans up after Miao Panel was closed while running
// something: those runs will never report back.
func (a *App) recoverInterrupted() {
	_ = a.Store.MarkInterrupted()
	plans, err := a.Store.ListPlans(1000)
	if err != nil {
		return
	}
	for _, p := range plans {
		if p.Status != core.PlanRunning {
			continue
		}
		v := viewOf(p)
		for i := range v.StepList {
			st := &v.StepList[i]
			if st.Status == "queued" || st.Status == "running" {
				st.Status = store.ExecInterrupted
				st.Log = append(st.Log, "Miao Panel 在执行过程中被关闭了，这一步的结果未知，请重新识别服务器确认")
			}
		}
		v.Status = core.PlanPartial
		_ = a.savePlan(&v)
	}
}
