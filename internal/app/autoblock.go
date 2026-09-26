package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
)

// Automatic blocking is a rule the user turns on: after each background
// refresh of the statistics, Miao Panel blocks the IPs the rule picks in
// EdgeOne, for a while, and lifts the block when it runs out. Each block
// and unblock is an ordinary checklist (so it shows in 建议 and the
// execution log and can be undone); only the confirmation is skipped.

// AutoBlockSettings is the rule.
type AutoBlockSettings struct {
	Enabled bool `json:"enabled"`
	// Level: "high" blocks high-risk IPs; "medium" medium and high ones.
	Level string `json:"level"`
	// RequireAI blocks only IPs the AI also says to block; each IP is
	// asked about at most once a day.
	RequireAI bool `json:"requireAI"`
	// Hours a block lasts; 0 keeps it until unblocked by hand.
	Hours int `json:"hours"`
	// Allow lists IPs and networks never blocked automatically.
	Allow []string `json:"allow"`
}

// AutoBlocked is an IP the rule blocked and has not lifted yet.
type AutoBlocked struct {
	IP     string `json:"ip"`
	Zone   string `json:"zone"`
	At     string `json:"at"`
	Until  string `json:"until,omitempty"` // empty: until unblocked by hand
	Reason string `json:"reason"`
	PlanID int64  `json:"planId"`
}

// AutoBlockEvent is one thing the rule did, for the page.
type AutoBlockEvent struct {
	At     string `json:"at"`
	Text   string `json:"text"`
	PlanID int64  `json:"planId,omitempty"`
}

type judged struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// AutoBlockState is the rule, what it has blocked and what it did lately.
type AutoBlockState struct {
	Settings AutoBlockSettings `json:"settings"`
	Blocked  []AutoBlocked     `json:"blocked"`
	Events   []AutoBlockEvent  `json:"events"`
	LastRun  string            `json:"lastRun,omitempty"`
	LastNote string            `json:"lastNote,omitempty"`
	Judged   map[string]judged `json:"judged,omitempty"`
	// Lifted: when each IP's automatic block ran out (local time, like the
	// statistics). It is blocked again only if it came back after that.
	Lifted map[string]string `json:"lifted,omitempty"`
}

const (
	autoBlockKey    = "autoblock"
	autoBlockPerRun = 20                 // most IPs blocked by one run
	autoBlockEvents = 30                 // events kept
	judgedTTL       = 24 * time.Hour     // how long an AI verdict is reused
	liftedTTL       = 7 * 24 * time.Hour // how long a lifted block is remembered
	localStamp      = "2006-01-02 15:04:05"
	planWait        = 3 * time.Minute     // how long a run waits for its checklist
	autoBlockLevel  = visits.RiskHigh     // the default level
	autoBlockHours  = 24                  // the default duration
	autoBlockUntil  = "2006-01-02T15:04Z" // stored times, UTC
)

var autoBlockMu sync.Mutex // one run at a time, and no lost updates

func (a *App) loadAutoBlock() AutoBlockState {
	st := AutoBlockState{Settings: AutoBlockSettings{Level: autoBlockLevel, RequireAI: true, Hours: autoBlockHours}}
	if v, _ := a.Store.Setting(autoBlockKey); v != "" {
		_ = json.Unmarshal([]byte(v), &st)
	}
	if st.Blocked == nil {
		st.Blocked = []AutoBlocked{}
	}
	if st.Events == nil {
		st.Events = []AutoBlockEvent{}
	}
	return st
}

func (a *App) saveAutoBlock(st AutoBlockState) error {
	if len(st.Events) > autoBlockEvents {
		st.Events = st.Events[len(st.Events)-autoBlockEvents:]
	}
	for ip, j := range st.Judged {
		if t, err := time.Parse(time.RFC3339, j.At); err != nil || time.Since(t) > judgedTTL {
			delete(st.Judged, ip)
		}
	}
	for ip, at := range st.Lifted {
		if t, err := time.ParseInLocation(localStamp, at, time.Local); err != nil || time.Since(t) > liftedTTL {
			delete(st.Lifted, ip)
		}
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return a.Store.SetSetting(autoBlockKey, string(data))
}

// AutoBlock returns the rule and its state.
func (a *App) AutoBlock() AutoBlockState {
	autoBlockMu.Lock()
	defer autoBlockMu.Unlock()
	st := a.loadAutoBlock()
	st.Judged = nil // internal
	return st
}

// SaveAutoBlock changes the rule.
func (a *App) SaveAutoBlock(s AutoBlockSettings) (AutoBlockState, error) {
	if s.Level != visits.RiskHigh && s.Level != visits.RiskMedium {
		return AutoBlockState{}, userErr("封禁范围只能是高风险，或者高风险和中风险")
	}
	if s.Hours < 0 || s.Hours > 24*365 {
		return AutoBlockState{}, userErr("封禁时长不对")
	}
	var allow []string
	for _, f := range s.Allow {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if net.ParseIP(f) == nil {
			if _, _, err := net.ParseCIDR(f); err != nil {
				return AutoBlockState{}, userErr("「%s」不是 IP 地址或网段", f)
			}
		}
		allow = append(allow, f)
	}
	s.Allow = allow
	if s.Enabled && a.tencentClient() == nil {
		return AutoBlockState{}, userErr("自动封禁通过 EdgeOne 进行，请先在「设置 → 腾讯云」填写密钥")
	}
	autoBlockMu.Lock()
	st := a.loadAutoBlock()
	was := st.Settings.Enabled
	st.Settings = s
	if s.Enabled != was {
		text := "关闭了自动封禁（已经封禁的会按原来的时间解封）"
		if s.Enabled {
			text = "开启了自动封禁：" + autoRuleText(s)
		}
		st.Events = append(st.Events, AutoBlockEvent{At: now(), Text: text})
	}
	err := a.saveAutoBlock(st)
	autoBlockMu.Unlock()
	if err != nil {
		return AutoBlockState{}, err
	}
	_ = a.Store.Audit("user", "settings.autoblock", onOff(s.Enabled), autoRuleText(s))
	return a.AutoBlock(), nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// autoRuleText is the rule in words.
func autoRuleText(s AutoBlockSettings) string {
	who := "高风险 IP"
	if s.Level == visits.RiskMedium {
		who = "高风险和中风险 IP"
	}
	if s.RequireAI {
		who += "（AI 也建议封禁的）"
	}
	return who + "，" + durationText(s.Hours)
}

func durationText(hours int) string {
	switch {
	case hours == 0:
		return "一直封禁，直到手动解封"
	case hours%24 == 0:
		return fmt.Sprintf("封禁 %d 天", hours/24)
	}
	return fmt.Sprintf("封禁 %d 小时", hours)
}

// forgetAutoBlocked hands IPs over to the user: a block or unblock they
// chose themselves is not undone when the automatic one runs out.
func (a *App) forgetAutoBlocked(ips []string) {
	want := map[string]bool{}
	for _, ip := range ips {
		want[strings.TrimSpace(ip)] = true
	}
	autoBlockMu.Lock()
	defer autoBlockMu.Unlock()
	st := a.loadAutoBlock()
	kept := st.Blocked[:0]
	changed := false
	for _, b := range st.Blocked {
		if want[b.IP] {
			changed = true
			continue
		}
		kept = append(kept, b)
	}
	if changed {
		st.Blocked = kept
		_ = a.saveAutoBlock(st)
	}
}

// allowed says whether ip is on the never-block list.
func allowed(list []string, ip string) bool {
	addr := net.ParseIP(ip)
	for _, f := range list {
		if f == ip {
			return true
		}
		if _, n, err := net.ParseCIDR(f); err == nil && addr != nil && n.Contains(addr) {
			return true
		}
	}
	return false
}

// RunAutoBlock applies the rule once: lifts blocks that ran out, then
// blocks what the rule picks from the latest statistics. It returns what
// it did, in words.
func (a *App) RunAutoBlock(ctx context.Context) string {
	autoBlockMu.Lock()
	st := a.loadAutoBlock()
	autoBlockMu.Unlock()
	if len(st.Blocked) == 0 && !st.Settings.Enabled {
		return "自动封禁没有开启"
	}
	var notes []string
	if n := a.liftExpired(ctx); n != "" {
		notes = append(notes, n)
	}
	if st.Settings.Enabled {
		notes = append(notes, a.blockPicked(ctx, st.Settings))
	}
	note := strings.Join(notes, "；")
	autoBlockMu.Lock()
	st = a.loadAutoBlock()
	st.LastRun, st.LastNote = now(), note
	_ = a.saveAutoBlock(st)
	autoBlockMu.Unlock()
	return note
}

// liftExpired unblocks what ran out, per EdgeOne site.
func (a *App) liftExpired(ctx context.Context) string {
	autoBlockMu.Lock()
	st := a.loadAutoBlock()
	autoBlockMu.Unlock()
	nowT := time.Now().UTC()
	due := map[string][]string{}
	for _, b := range st.Blocked {
		if t, err := time.Parse(autoBlockUntil, b.Until); b.Until != "" && err == nil && !t.After(nowT) {
			due[b.Zone] = append(due[b.Zone], b.IP)
		}
	}
	if len(due) == 0 {
		return ""
	}
	// Only lift what is still blocked (the user may have unblocked it).
	current := map[string]map[string]bool{}
	if list, err := a.Blocked(ctx); err == nil {
		for _, z := range list {
			current[z.Zone] = map[string]bool{}
			for _, ip := range z.IPs {
				current[z.Zone][ip] = true
			}
		}
	} else {
		return "读取 EdgeOne 封禁列表失败，到期的封禁下次再解：" + err.Error()
	}
	var lifted []string
	var failed []string
	zones := make([]string, 0, len(due))
	for z := range due {
		zones = append(zones, z)
	}
	sort.Strings(zones)
	for _, zone := range zones {
		var ips []string
		for _, ip := range due[zone] {
			if current[zone][ip] {
				ips = append(ips, ip)
			}
		}
		if len(ips) == 0 {
			a.dropAutoBlocked(zone, due[zone], "")
			continue
		}
		plan, err := a.unblockPlan(ctx, "auto", zone, ips, "自动封禁到期，按规则解封")
		if err == nil {
			plan, err = a.runAuto(ctx, plan)
		}
		if err != nil {
			failed = append(failed, zone+"："+err.Error())
			continue
		}
		a.dropAutoBlocked(zone, due[zone], fmt.Sprintf("到期解封 %s 的 %d 个 IP：%s", zone, len(ips), strings.Join(ips, "、")))
		a.noteAutoPlan(plan.ID)
		lifted = append(lifted, ips...)
	}
	var out []string
	if len(lifted) > 0 {
		out = append(out, fmt.Sprintf("到期解封 %d 个 IP", len(lifted)))
	}
	if len(failed) > 0 {
		out = append(out, "解封失败："+strings.Join(failed, "；"))
	}
	return strings.Join(out, "；")
}

func (a *App) dropAutoBlocked(zone string, ips []string, event string) {
	gone := map[string]bool{}
	for _, ip := range ips {
		gone[ip] = true
	}
	autoBlockMu.Lock()
	defer autoBlockMu.Unlock()
	st := a.loadAutoBlock()
	kept := st.Blocked[:0]
	if st.Lifted == nil {
		st.Lifted = map[string]string{}
	}
	for _, b := range st.Blocked {
		if b.Zone == zone && gone[b.IP] {
			st.Lifted[b.IP] = time.Now().Format(localStamp)
			continue
		}
		kept = append(kept, b)
	}
	st.Blocked = kept
	if event != "" {
		st.Events = append(st.Events, AutoBlockEvent{At: now(), Text: event})
	}
	_ = a.saveAutoBlock(st)
}

// noteAutoPlan puts the checklist's id on the latest event.
func (a *App) noteAutoPlan(id int64) {
	autoBlockMu.Lock()
	defer autoBlockMu.Unlock()
	st := a.loadAutoBlock()
	if n := len(st.Events); n > 0 {
		st.Events[n-1].PlanID = id
		_ = a.saveAutoBlock(st)
	}
}

// blockPicked blocks the IPs the rule picks in every source's latest
// statistics (today's).
func (a *App) blockPicked(ctx context.Context, s AutoBlockSettings) string {
	if a.tencentClient() == nil {
		return "腾讯云密钥没有填写，没法封禁"
	}
	blockedNow := map[string]bool{}
	list, err := a.Blocked(ctx)
	if err != nil {
		return "读取 EdgeOne 封禁列表失败：" + err.Error()
	}
	for _, z := range list {
		for _, ip := range z.IPs {
			blockedNow[ip] = true
		}
	}
	sources, err := a.VisitSources()
	if err != nil {
		return err.Error()
	}
	autoBlockMu.Lock()
	lifted := a.loadAutoBlock().Lifted
	autoBlockMu.Unlock()
	var did []string
	total := 0
	for _, src := range sources {
		if total >= autoBlockPerRun {
			break
		}
		v, err := a.LatestVisits(ctx, src.Key)
		if err != nil || v.Range(1) == nil {
			continue
		}
		var picked []visits.IPProfile
		for _, p := range v.Range(1).IPs {
			switch {
			case blockedNow[p.IP], p.EdgeOne, p.Crawler != "", visits.Private(p.IP), allowed(s.Allow, p.IP):
				continue
			case lifted[p.IP] != "" && p.Last <= lifted[p.IP]: // not back since its block ran out
				continue
			case p.Risk == visits.RiskHigh, s.Level == visits.RiskMedium && p.Risk == visits.RiskMedium:
				picked = append(picked, p)
			}
		}
		if len(picked) == 0 {
			continue
		}
		if s.RequireAI {
			var err error
			if picked, err = a.aiAgrees(ctx, src.Key, picked); err != nil {
				did = append(did, src.Title+"：AI 研判失败，这次没有封禁（"+err.Error()+"）")
				continue
			}
		}
		if len(picked) > autoBlockPerRun-total {
			picked = picked[:autoBlockPerRun-total]
		}
		if len(picked) == 0 {
			continue
		}
		ips := make([]string, len(picked))
		why := map[string]string{}
		for i, p := range picked {
			ips[i] = p.IP
			why[p.IP] = strings.Join(p.Reasons, "；")
		}
		plan, err := a.blockPlan(ctx, "auto", src.Key, ips, fmt.Sprintf("自动封禁 %d 个%s", len(ips), levelWords(s.Level)),
			"按自动封禁规则（"+autoRuleText(s)+"），根据"+src.Title+"：")
		if err != nil {
			did = append(did, src.Title+"："+err.Error())
			continue
		}
		plan, err = a.runAuto(ctx, plan)
		if err != nil {
			did = append(did, src.Title+"：封禁没有完成："+err.Error())
			continue
		}
		n := a.recordAutoBlocked(plan, s, why)
		for _, ip := range ips {
			blockedNow[ip] = true
		}
		total += n
		if n > 0 {
			did = append(did, fmt.Sprintf("封禁了 %d 个 IP", n))
		}
	}
	if len(did) == 0 {
		return "没有需要封禁的 IP"
	}
	return strings.Join(did, "；")
}

func levelWords(level string) string {
	if level == visits.RiskMedium {
		return "可疑 IP"
	}
	return "高风险 IP"
}

// aiAgrees keeps the IPs the AI says to block, asking only about those
// without a verdict from the last day.
func (a *App) aiAgrees(ctx context.Context, source string, picked []visits.IPProfile) ([]visits.IPProfile, error) {
	autoBlockMu.Lock()
	st := a.loadAutoBlock()
	autoBlockMu.Unlock()
	var ask []string
	for _, p := range picked {
		if _, ok := st.Judged[p.IP]; !ok {
			ask = append(ask, p.IP)
		}
	}
	if len(ask) > 0 {
		j, err := a.JudgeIPs(ctx, source, 1, ask)
		if err != nil {
			return nil, err
		}
		autoBlockMu.Lock()
		st = a.loadAutoBlock()
		if st.Judged == nil {
			st.Judged = map[string]judged{}
		}
		for _, v := range j.Verdicts {
			st.Judged[v.IP] = judged{Action: v.Action, Reason: v.Reason, At: now()}
		}
		// An IP the AI did not answer about is not asked again today.
		for _, ip := range ask {
			if _, ok := st.Judged[ip]; !ok {
				st.Judged[ip] = judged{Action: "watch", Reason: "AI 没有给出结论", At: now()}
			}
		}
		_ = a.saveAutoBlock(st)
		autoBlockMu.Unlock()
	}
	var out []visits.IPProfile
	for _, p := range picked {
		if j := st.Judged[p.IP]; j.Action == "block" {
			if j.Reason != "" {
				p.Reasons = append([]string{"AI：" + j.Reason}, p.Reasons...)
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// runAuto executes every step of a checklist the rule made and waits for
// it to finish.
func (a *App) runAuto(ctx context.Context, plan PlanView) (PlanView, error) {
	all := make([]int, len(plan.StepList))
	for i := range all {
		all[i] = i
	}
	if _, err := a.executePlan(plan.ID, all, "auto"); err != nil {
		return plan, err
	}
	deadline := time.Now().Add(planWait)
	for {
		v, err := a.Plan(plan.ID)
		if err != nil {
			return plan, err
		}
		if v.Status != core.PlanRunning {
			if v.Status != core.PlanDone {
				return v, fmt.Errorf("清单「%s」没有全部完成", v.Title)
			}
			return v, nil
		}
		if time.Now().After(deadline) {
			return v, fmt.Errorf("清单「%s」还没执行完", v.Title)
		}
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// recordAutoBlocked remembers the IPs a finished checklist blocked and
// when they are due to be lifted; it returns how many.
func (a *App) recordAutoBlocked(plan PlanView, s AutoBlockSettings, why map[string]string) int {
	at := time.Now().UTC()
	until := ""
	if s.Hours > 0 {
		until = at.Add(time.Duration(s.Hours) * time.Hour).Format(autoBlockUntil)
	}
	autoBlockMu.Lock()
	defer autoBlockMu.Unlock()
	st := a.loadAutoBlock()
	n := 0
	var ips []string
	for _, step := range plan.StepList {
		if step.Capability != "eo.ip.block" || step.Status != actions.StatusDone {
			continue
		}
		zone := fmt.Sprint(step.Params["domain"])
		for _, ip := range strings.Split(fmt.Sprint(step.Params["ips"]), ",") {
			if ip = strings.TrimSpace(ip); ip == "" {
				continue
			}
			st.Blocked = append(st.Blocked, AutoBlocked{IP: ip, Zone: zone, At: at.Format(autoBlockUntil), Until: until, Reason: why[ip], PlanID: plan.ID})
			ips = append(ips, ip)
			n++
		}
	}
	if n > 0 {
		st.Events = append(st.Events, AutoBlockEvent{At: now(), PlanID: plan.ID,
			Text: fmt.Sprintf("自动封禁 %d 个 IP（%s）：%s", n, durationText(s.Hours), strings.Join(ips, "、"))})
	}
	_ = a.saveAutoBlock(st)
	return n
}
