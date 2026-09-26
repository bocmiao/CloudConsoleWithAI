package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/profile"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// runTimeout bounds one plan run.
const runTimeout = 30 * time.Minute

// serverLocks makes sure only one plan changes a server at a time.
type serverLocks struct {
	mu   sync.Mutex
	busy map[int64]bool
}

func (l *serverLocks) try(id int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy == nil {
		l.busy = map[int64]bool{}
	}
	if l.busy[id] {
		return false
	}
	l.busy[id] = true
	return true
}

func (l *serverLocks) release(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.busy, id)
}

// prepareSteps fills in, for each step, whether it can run on the server
// and how. Risk always comes from policy, never from the AI.
func prepareSteps(steps []core.Step, adapter string) []core.Step {
	for i := range steps {
		s := &steps[i]
		s.Risk = core.RiskOf(s.Capability)
		s.Executable, s.Blocked, s.Status, s.Log, s.Undo, s.LogID = false, "", "", nil, nil, 0
		r, err := actions.Resolve(s.Capability, s.Params, adapter)
		var ne *actions.ErrNotExecutable
		switch {
		case errors.As(err, &ne):
			s.Blocked = err.Error()
			continue
		case err != nil:
			s.Blocked = "参数不对（" + err.Error() + "），请让 AI 重新生成这份清单"
			continue
		case adapter == noServer && r.Impl.NeedsServer():
			s.Blocked = "这一步要在服务器上执行，但这份清单没有指定服务器，请让 AI 重新生成"
			continue
		}
		s.Executable = true
		s.Title = r.Cap.Title
		s.Risk = r.Cap.Risk
		s.Via = r.Impl.Via
		s.Downtime = r.Impl.Downtime
		s.Reversible = r.Cap.Reversible
		if s.Capability == freeCapability && (s.Free == nil || !s.Free.Passed) {
			s.Executable = false
			s.Blocked = "这段自定义命令还没有通过试运行和独立审查，请让 AI 重新生成"
			if s.Free != nil && s.Free.Problem != "" {
				s.Blocked = s.Free.Problem
			}
		}
	}
	return steps
}

// PlanView is a plan with its steps decoded.
type PlanView struct {
	store.Plan
	StepList []core.Step `json:"stepList"`
	Before   *Snapshot   `json:"before,omitempty"`
	After    *Snapshot   `json:"after,omitempty"`
}

// Snapshot is the handful of numbers shown before and after a run.
type Snapshot struct {
	MemAvailableMB int    `json:"memAvailableMB"`
	MemTotalMB     int    `json:"memTotalMB"`
	SwapMB         int    `json:"swapMB"`
	RootDiskPct    int    `json:"rootDiskPct"`
	Findings       int    `json:"findings"`
	FailedServices string `json:"failedServices"`
}

func snapshotOf(p *profile.Profile) *Snapshot {
	s := &Snapshot{
		MemAvailableMB: p.Memory.AvailableMB, MemTotalMB: p.Memory.TotalMB, SwapMB: p.Memory.SwapTotalMB,
		Findings: len(p.Findings),
	}
	for _, d := range p.Disks {
		if d.Mount == "/" {
			s.RootDiskPct = d.UsePct
		}
	}
	for _, f := range p.Findings {
		if f.Title == "有服务启动失败" {
			s.FailedServices = f.Detail
		}
	}
	return s
}

type planResult struct {
	Before *Snapshot `json:"before"`
	After  *Snapshot `json:"after"`
}

func viewOf(p store.Plan) PlanView {
	v := PlanView{Plan: p}
	_ = json.Unmarshal([]byte(p.Steps), &v.StepList)
	if p.Result != "" {
		var r planResult
		if json.Unmarshal([]byte(p.Result), &r) == nil {
			v.Before, v.After = r.Before, r.After
		}
	}
	return v
}

// recheck re-evaluates the steps that have not run yet against this
// version of Miao Panel and the server's current environment, so a
// checklist made before an upgrade shows what can run now.
func (a *App) recheck(v *PlanView, adapters map[int64]string) {
	adapter, ok := adapters[v.ServerID]
	if !ok {
		sv, err := a.planServer(v.ServerID)
		if err != nil {
			return
		}
		adapter, adapters[v.ServerID] = sv.Adapter, sv.Adapter
	}
	for i, st := range v.StepList {
		if st.Status == "" {
			v.StepList[i] = prepareSteps([]core.Step{st}, adapter)[0]
		}
	}
}

// Plan returns one plan with decoded steps.
func (a *App) Plan(id int64) (PlanView, error) {
	p, err := a.Store.GetPlan(id)
	if err != nil {
		return PlanView{}, userErr("找不到这个清单（编号 %d）", id)
	}
	v := viewOf(p)
	a.recheck(&v, map[int64]string{})
	return v, nil
}

// Plans lists recent plans with decoded steps.
func (a *App) Plans(limit int) ([]PlanView, error) {
	list, err := a.Store.ListPlans(limit)
	if err != nil {
		return nil, err
	}
	adapters := map[int64]string{}
	out := make([]PlanView, 0, len(list))
	for _, p := range list {
		v := viewOf(p)
		a.recheck(&v, adapters)
		out = append(out, v)
	}
	return out, nil
}

func (a *App) savePlan(v *PlanView) error {
	data, err := json.Marshal(v.StepList)
	if err != nil {
		return err
	}
	v.Steps = string(data)
	if v.Before != nil || v.After != nil {
		r, _ := json.Marshal(planResult{Before: v.Before, After: v.After})
		v.Result = string(r)
	}
	return a.Store.UpdatePlan(v.Plan)
}

// env opens what running actions on a server needs.
func (a *App) env(ctx context.Context, id int64) (store.Server, *actions.Env, error) {
	sv, c, err := a.connect(ctx, id)
	if err != nil {
		return sv, nil, err
	}
	env := &actions.Env{SSH: c, User: sv.Username, PollInterval: a.PollInterval}
	env.Reconnect = func(ctx context.Context) (sshx.Conn, error) {
		_, nc, err := a.connect(ctx, id)
		return nc, err
	}
	if raw, _, err := a.Store.GetProfile(id); err == nil {
		env.PanelApps = profile.Parse(raw).Panel.Apps
	}
	// Configured or not, the 1Panel API is only used by 1Panel operations.
	if env.OnePanel, err = a.onePanelClient(id, c); err != nil {
		c.Close()
		return sv, nil, err
	}
	env.Cloud = a.tencentClient()
	return sv, env, nil
}

// cloudServer stands in for the server of checklists that only change
// Tencent Cloud.
var cloudServer = store.Server{Name: "腾讯云", Adapter: noServer}

// noServer is the adapter of checklists without a server.
const noServer = "-"

// planServer returns a checklist's server; 0 means none.
func (a *App) planServer(id int64) (store.Server, error) {
	if id == 0 {
		return cloudServer, nil
	}
	return a.Store.GetServer(id)
}

// discoverWith refreshes the saved profile over an open connection.
func (a *App) discoverWith(ctx context.Context, sv store.Server, c sshx.Conn, title string) (*profile.Profile, error) {
	res, err := a.runDiscover(ctx, sv, c, nil, title)
	if err != nil || strings.TrimSpace(res.Stdout) == "" {
		return nil, fmt.Errorf("识别失败：%v", err)
	}
	prof := profile.Parse(res.Stdout)
	if err := a.Store.SaveProfile(sv.ID, res.Stdout, prof.Adapter); err != nil {
		return prof, err
	}
	return prof, nil
}

// ExecutePlan starts running the selected steps in the background; poll
// Plan to follow progress.
func (a *App) ExecutePlan(id int64, selected []int) (PlanView, error) {
	v, err := a.Plan(id)
	if err != nil {
		return v, err
	}
	if v.Status == core.PlanRunning {
		return v, userErr("这个清单正在执行中")
	}
	if len(selected) == 0 {
		return v, userErr("请至少勾选一项")
	}
	sv, err := a.planServer(v.ServerID)
	if err != nil {
		return v, userErr("这个清单对应的服务器已经被删除了")
	}
	// Re-check against the current environment: it may have changed since
	// the AI proposed the plan.
	fresh := prepareSteps(append([]core.Step(nil), v.StepList...), sv.Adapter)
	seen := map[int]bool{}
	for _, i := range selected {
		if i < 0 || i >= len(v.StepList) || seen[i] {
			return v, userErr("勾选的步骤不对")
		}
		seen[i] = true
		if v.StepList[i].Status == actions.StatusDone {
			return v, userErr("第 %d 步已经执行过了", i+1)
		}
		if !fresh[i].Executable {
			return v, userErr("第 %d 步不能执行：%s", i+1, fresh[i].Blocked)
		}
	}
	for _, i := range selected {
		if err := a.freeReady(v.StepList, i, seen); err != nil {
			return v, err
		}
	}
	if !a.locks.try(sv.ID) {
		return v, userErr("这台服务器上正在执行其他清单，请等它完成")
	}
	for i := range v.StepList {
		if seen[i] {
			st := fresh[i]
			st.Status = "queued"
			v.StepList[i] = st
		}
	}
	v.Status = core.PlanRunning
	if err := a.savePlan(&v); err != nil {
		a.locks.release(sv.ID)
		return v, err
	}
	_ = a.Store.Audit("user", "plan.execute", v.Title, fmt.Sprintf("服务器 %s，%d 项", sv.Name, len(selected)))
	go a.runPlan(v.ID, sv.ID)
	return v, nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (a *App) runPlan(planID, serverID int64) {
	defer a.locks.release(serverID)
	defer a.forgetCertificates() // a step may have issued or changed one
	ctx, cancel := context.WithTimeout(withOrigin(context.Background(), OriginPlan), runTimeout)
	defer cancel()
	v, err := a.Plan(planID)
	if err != nil {
		return
	}
	fail := func(msg string) {
		for i := range v.StepList {
			if v.StepList[i].Status == "queued" {
				v.StepList[i].Status = "skipped"
				v.StepList[i].Log = append(v.StepList[i].Log, msg)
			}
		}
		v.Status = core.PlanPartial
		_ = a.savePlan(&v)
	}
	sv, err := a.planServer(serverID)
	if err != nil {
		fail("这个清单对应的服务器已经被删除了")
		return
	}
	// Only connect to the server when a step works on it; Tencent Cloud
	// steps need nothing but the API.
	needServer := false
	for _, st := range v.StepList {
		if r, err := actions.Resolve(st.Capability, st.Params, sv.Adapter); st.Status == "queued" && err == nil && r.Impl.NeedsServer() {
			needServer = true
		}
	}
	env := &actions.Env{Cloud: a.tencentClient(), PollInterval: a.PollInterval}
	if needServer {
		if sv, env, err = a.env(ctx, serverID); err != nil {
			fail(friendlySSHError(err).Error())
			return
		}
		defer func() { env.SSH.Close() }()
		if prof, err := a.discoverWith(ctx, sv, env.SSH, "执行前识别（记录修改前的状态）"); err == nil {
			v.Before = snapshotOf(prof)
			env.PanelApps = prof.Panel.Apps
		}
	}
	ok := true
	for i := range v.StepList {
		st := &v.StepList[i]
		if st.Status != "queued" {
			continue
		}
		if !ok {
			st.Status = "skipped"
			st.Log = []string{"前面的步骤没有成功，为了安全没有继续执行"}
			continue
		}
		r, err := actions.Resolve(st.Capability, st.Params, sv.Adapter)
		if err != nil {
			st.Status, st.Log, ok = actions.StatusRefused, []string{err.Error()}, false
			continue
		}
		title := r.Cap.Title
		if pt := paramText(r.Values); pt != "" {
			title += "（" + pt + "）"
		}
		entry := a.startExec(store.ExecLog{
			ServerID: sv.ID, ServerName: sv.Name, Adapter: sv.Adapter, Origin: OriginPlan, Kind: store.ExecChange,
			Title: title, Note: st.Summary, Capability: st.Capability, Params: st.Params, Via: r.Impl.Via,
			Reversible: r.Cap.Reversible, PlanID: v.ID, StepIdx: i,
		})
		st.Status, st.LogID = "running", entry.ID
		_ = a.savePlan(&v)
		out := actions.Apply(ctx, env, r, func(log []string) {
			st.Log = log
			_ = a.savePlan(&v)
		})
		a.finishAction(&entry, out)
		st.Status, st.Log, st.Undo, st.FinishedAt = out.Status, out.Log, out.Undo, now()
		if out.Status != actions.StatusDone {
			ok = false
		}
		_ = a.savePlan(&v)
		_ = a.Store.Audit("system", "plan.step", st.Title, fmt.Sprintf("%s：%s", sv.Name, out.Status))
	}
	if needServer {
		if prof, err := a.discoverWith(ctx, sv, env.SSH, "执行后识别（对比效果）"); err == nil {
			v.After = snapshotOf(prof)
		}
	}
	v.Status = core.PlanDone
	if !ok {
		v.Status = core.PlanPartial
	}
	_ = a.savePlan(&v)
}

// UndoStep reverts one executed step.
func (a *App) UndoStep(ctx context.Context, planID int64, idx int) (PlanView, error) {
	v, err := a.Plan(planID)
	if err != nil {
		return v, err
	}
	if idx < 0 || idx >= len(v.StepList) {
		return v, userErr("步骤编号不对")
	}
	st := v.StepList[idx]
	if st.Status != actions.StatusDone || !st.Reversible {
		return v, userErr("这一步不能撤销")
	}
	sv, err := a.planServer(v.ServerID)
	if err != nil {
		return v, userErr("这个清单对应的服务器已经被删除了")
	}
	if !a.locks.try(sv.ID) {
		return v, userErr("这台服务器上正在执行其他操作，请稍后再试")
	}
	defer a.locks.release(sv.ID)
	var e store.ExecLog
	if st.LogID > 0 {
		if e, err = a.Store.GetExec(st.LogID); err != nil {
			return v, err
		}
	} else {
		// Run before the execution log existed: the step has what we need.
		e = store.ExecLog{
			ServerID: sv.ID, ServerName: sv.Name, Adapter: sv.Adapter, Kind: store.ExecChange, Title: st.Title,
			Capability: st.Capability, Params: st.Params, Via: st.Via, Status: st.Status, Undo: st.Undo,
			Reversible: st.Reversible, PlanID: planID, StepIdx: idx,
		}
	}
	if e.UndoneBy > 0 {
		return v, userErr("这一步已经回滚过了")
	}
	_, rerr := a.rollback(ctx, &e)
	nv, err := a.Plan(planID)
	if rerr != nil {
		return nv, rerr
	}
	return nv, err
}

// UndoPlan reverts every executed, reversible step of a plan, newest
// first, and stops at the first failure.
func (a *App) UndoPlan(ctx context.Context, planID int64) (PlanView, error) {
	v, err := a.Plan(planID)
	if err != nil {
		return v, err
	}
	if v.Status == core.PlanRunning {
		return v, userErr("这个清单正在执行中")
	}
	var idxs []int
	for i, st := range v.StepList {
		if st.Status == actions.StatusDone && st.Reversible {
			idxs = append(idxs, i)
		}
	}
	if len(idxs) == 0 {
		return v, userErr("这份清单里没有可以撤销的步骤")
	}
	for j := len(idxs) - 1; j >= 0; j-- {
		if _, err := a.UndoStep(ctx, planID, idxs[j]); err != nil {
			nv, _ := a.Plan(planID)
			return nv, userErr("第 %d 步撤销失败，已停止：%v", idxs[j]+1, err)
		}
	}
	return a.Plan(planID)
}
