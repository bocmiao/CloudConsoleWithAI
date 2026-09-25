package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/profile"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/scripts"
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
		s.Executable, s.Blocked, s.Status, s.Log, s.Undo = false, "", "", nil, nil
		r, err := actions.Resolve(s.Capability, s.Params, adapter)
		if err != nil {
			s.Blocked = err.Error()
			continue
		}
		s.Executable = true
		s.Title = r.Cap.Title
		s.Risk = r.Cap.Risk
		s.Via = r.Impl.Via
		s.Downtime = r.Impl.Downtime
		s.Reversible = r.Cap.Reversible
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

// Plan returns one plan with decoded steps.
func (a *App) Plan(id int64) (PlanView, error) {
	p, err := a.Store.GetPlan(id)
	if err != nil {
		return PlanView{}, userErr("找不到这个清单（编号 %d）", id)
	}
	return viewOf(p), nil
}

// Plans lists recent plans with decoded steps.
func (a *App) Plans(limit int) ([]PlanView, error) {
	list, err := a.Store.ListPlans(limit)
	if err != nil {
		return nil, err
	}
	out := make([]PlanView, 0, len(list))
	for _, p := range list {
		out = append(out, viewOf(p))
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
	env := &actions.Env{SSH: c, User: sv.Username}
	env.Reconnect = func(ctx context.Context) (*sshx.Client, error) {
		_, nc, err := a.connect(ctx, id)
		return nc, err
	}
	if raw, _, err := a.Store.GetProfile(id); err == nil {
		env.PanelApps = profile.Parse(raw).Panel.Apps
	}
	if sv.Adapter == "1panel" {
		if env.OnePanel, err = a.onePanelClient(id, c); err != nil {
			c.Close()
			return sv, nil, err
		}
	}
	return sv, env, nil
}

// discoverWith refreshes the saved profile over an open connection.
func (a *App) discoverWith(ctx context.Context, sv store.Server, c *sshx.Client) (*profile.Profile, error) {
	res, err := c.RunScript(ctx, sv.Username, scripts.Discover, nil, maxDiscoverOutput)
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
	sv, err := a.Store.GetServer(v.ServerID)
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
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
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
	sv, env, err := a.env(ctx, serverID)
	if err != nil {
		fail(friendlySSHError(err).Error())
		return
	}
	defer func() { env.SSH.Close() }()

	if prof, err := a.discoverWith(ctx, sv, env.SSH); err == nil {
		v.Before = snapshotOf(prof)
		env.PanelApps = prof.Panel.Apps
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
		st.Status = "running"
		_ = a.savePlan(&v)
		out := actions.Apply(ctx, env, r, func(log []string) {
			st.Log = log
			_ = a.savePlan(&v)
		})
		st.Status, st.Log, st.Undo, st.FinishedAt = out.Status, out.Log, out.Undo, now()
		if out.Status != actions.StatusDone {
			ok = false
		}
		_ = a.savePlan(&v)
		_ = a.Store.Audit("system", "plan.step", st.Title, fmt.Sprintf("%s：%s", sv.Name, out.Status))
	}
	if prof, err := a.discoverWith(ctx, sv, env.SSH); err == nil {
		v.After = snapshotOf(prof)
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
	st := &v.StepList[idx]
	if st.Status != actions.StatusDone || !st.Reversible {
		return v, userErr("这一步不能撤销")
	}
	sv, err := a.Store.GetServer(v.ServerID)
	if err != nil {
		return v, userErr("这个清单对应的服务器已经被删除了")
	}
	if !a.locks.try(sv.ID) {
		return v, userErr("这台服务器上正在执行其他操作，请稍后再试")
	}
	defer a.locks.release(sv.ID)
	r, err := actions.Resolve(st.Capability, st.Params, sv.Adapter)
	if err != nil {
		return v, userErr("无法撤销：%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	sv, env, err := a.env(ctx, sv.ID)
	if err != nil {
		return v, friendlySSHError(err)
	}
	defer func() { env.SSH.Close() }()
	out := actions.Undo(ctx, env, r, st.Undo)
	st.Log = append(st.Log, out.Log...)
	if out.Status == actions.StatusUndone {
		st.Status = actions.StatusUndone
	}
	if prof, err := a.discoverWith(ctx, sv, env.SSH); err == nil {
		v.After = snapshotOf(prof)
	}
	if err := a.savePlan(&v); err != nil {
		return v, err
	}
	_ = a.Store.Audit("user", "plan.undo", st.Title, fmt.Sprintf("%s：%s", sv.Name, out.Status))
	if out.Status != actions.StatusUndone {
		return v, userErr("撤销没有成功：%s", strings.Join(out.Log, "；"))
	}
	return v, nil
}
