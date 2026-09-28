package actions

import (
	"context"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// Rolling a server's system disk back to a snapshot: the server is shut
// down (cleanly), the disk replaced by the snapshot, and the server
// started again if it was running. What was written after the snapshot is
// gone, so the page's checklist first snapshots the disk as it is now.

const rollbackNoUndo = "回滚会用快照替换整个系统盘，快照之后写入的数据会丢失，不能直接撤销；" +
	"想回到回滚前的样子，用回滚前创建的快照再回滚一次（页面生成的清单会先做这个快照）"

func init() {
	snap := Param{Name: "snapshot", Kind: "name", Required: true, Desc: "要回滚到的快照 ID（服务器详情里的快照列表）"}
	register(&Capability{
		Name: "cloud.snapshot.rollback", Title: "回滚腾讯云服务器到快照", Risk: core.R3, NoUndo: rollbackNoUndo,
		Params: []Param{{Name: "instance", Kind: "instance", Required: true, Desc: "腾讯云实例 ID"},
			{Name: "region", Kind: "region", Required: true, Desc: "实例所在地域"}, snap},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "snapshot_rollback",
			Downtime: "服务器会关机几分钟，系统盘换成快照时的样子，原来开着的回滚后自动开机"}},
	})
	register(&Capability{
		Name: "aliyun.snapshot.rollback", Title: "回滚阿里云服务器到快照", Risk: core.R3, NoUndo: rollbackNoUndo,
		Params: []Param{{Name: "instance", Kind: "aliinstance", Required: true, Desc: "阿里云实例 ID"},
			{Name: "region", Kind: "region", Required: true, Desc: "实例所在地域"}, snap},
		Impls: map[string]Impl{"*": {Via: "阿里云接口", Cloud: "ali_snapshot_rollback",
			Downtime: "服务器会关机几分钟，系统盘换成快照时的样子，原来开着的回滚后自动开机"}},
	})
}

func applyRollback(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	s, err := findInstance(ctx, c, v["region"], v["instance"])
	if err != nil {
		return refused(out, "%v", err)
	}
	list, err := c.Snapshots(ctx, s.Region, s)
	if err != nil {
		return refused(out, "读取快照失败：%v", err)
	}
	var sn *tencent.Snapshot
	for i := range list {
		if list[i].ID == v["snapshot"] {
			sn = &list[i]
		}
	}
	switch {
	case sn == nil:
		return refused(out, "%s 的系统盘没有快照 %s", serverText(s), v["snapshot"])
	case sn.State != "NORMAL":
		return refused(out, "快照 %s 现在的状态是 %s，要等它可用才能回滚", snapName(sn.Name, sn.ID), sn.State)
	case s.State != "RUNNING" && s.State != "STOPPED":
		return refused(out, "%s 当前状态是 %s，要等它运行或关机后再回滚", serverText(s), s.State)
	}
	running := s.State == "RUNNING"
	if running {
		report("正在关机 %s（回滚要先关机）", serverText(s))
		if err := c.Power(ctx, s.Region, s.ID, "StopInstances"); err != nil {
			return refused(out, "关机失败：%v", err)
		}
		if !waitTencentState(ctx, env, c, s, "STOPPED") {
			out.Status = StatusFailed
			out.logf("等了几分钟 %s 还没关机，没有回滚；请到腾讯云控制台查看", serverText(s))
			return *out
		}
	}
	report("正在把系统盘回滚到快照 %s", snapName(sn.Name, sn.ID))
	if err := c.ApplySnapshot(ctx, s.Region, s, sn.ID); err != nil {
		out.Status = StatusFailed
		out.logf("回滚失败：%v", err)
		if running {
			out.logf("服务器已经关机，%s", startAgain(ctx, env, c, s))
		}
		return *out
	}
	done, _ := waitFor(ctx, env, 120, func() (bool, error) {
		now, err := c.Snapshots(ctx, s.Region, s)
		for _, x := range now {
			if x.ID == sn.ID {
				return x.State == "NORMAL", err
			}
		}
		return false, err
	})
	if !done {
		out.Status = StatusFailed
		out.logf("回滚已经提交，但等了很久还没完成；请到腾讯云控制台查看，完成后再开机")
		return *out
	}
	report("系统盘已经回滚到快照 %s", snapName(sn.Name, sn.ID))
	if running {
		report("%s", startAgain(ctx, env, c, s))
	}
	out.Status, out.Undo = StatusDone, map[string]string{}
	return *out
}

func waitTencentState(ctx context.Context, env *Env, c *tencent.Client, s tencent.Server, want string) bool {
	ok, _ := waitFor(ctx, env, 36, func() (bool, error) {
		now, found, err := c.Instance(ctx, s.Region, s.ID)
		return found && now.State == want, err
	})
	return ok
}

// startAgain starts a server that was running before the rollback.
func startAgain(ctx context.Context, env *Env, c *tencent.Client, s tencent.Server) string {
	if err := c.Power(ctx, s.Region, s.ID, "StartInstances"); err != nil {
		return "重新开机失败：" + err.Error() + "，请到腾讯云控制台开机"
	}
	if !waitTencentState(ctx, env, c, s, "RUNNING") {
		return "已经发出开机指令，但还没确认开好，请稍后到腾讯云控制台查看"
	}
	return "已重新开机"
}

func aliRollback(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Aliyun
	s, err := aliFind(ctx, c, v["region"], v["instance"])
	if err != nil {
		return refused(out, "%v", err)
	}
	list, err := c.Snapshots(ctx, s)
	if err != nil {
		return refused(out, "读取快照失败：%v", err)
	}
	var sn *aliyun.Snapshot
	for i := range list {
		if list[i].ID == v["snapshot"] {
			sn = &list[i]
		}
	}
	switch {
	case sn == nil:
		return refused(out, "%s 的系统盘没有快照 %s", aliServerText(s), v["snapshot"])
	case sn.State != "ACCOMPLISHED":
		return refused(out, "快照 %s 还没有完成（%s），要等它完成才能回滚", snapName(sn.Name, sn.ID), sn.State)
	case s.State != "RUNNING" && s.State != "STOPPED":
		return refused(out, "%s 当前状态是 %s，要等它运行或关机后再回滚", aliServerText(s), s.State)
	}
	running := s.State == "RUNNING"
	if running {
		report("正在关机 %s（回滚要先关机）", aliServerText(s))
		if err := c.Power(ctx, s.Region, s.ID, "stop"); err != nil {
			return refused(out, "关机失败：%v", err)
		}
		if !waitAliState(ctx, env, c, s, "STOPPED") {
			out.Status = StatusFailed
			out.logf("等了几分钟 %s 还没关机，没有回滚；请到阿里云控制台查看", aliServerText(s))
			return *out
		}
	}
	report("正在把系统盘回滚到快照 %s", snapName(sn.Name, sn.ID))
	if err := c.ResetDisk(ctx, s, sn.ID); err != nil {
		out.Status = StatusFailed
		out.logf("回滚失败：%v", err)
		if running {
			out.logf("服务器已经关机，%s", aliStartAgain(ctx, env, c, s))
		}
		return *out
	}
	done, _ := waitFor(ctx, env, 120, func() (bool, error) {
		st, err := c.DiskStatus(ctx, s)
		return err == nil && !strings.EqualFold(st, "ReIniting"), err
	})
	if !done {
		out.Status = StatusFailed
		out.logf("回滚已经提交，但等了很久还没完成；请到阿里云控制台查看，完成后再开机")
		return *out
	}
	report("系统盘已经回滚到快照 %s", snapName(sn.Name, sn.ID))
	if running {
		report("%s", aliStartAgain(ctx, env, c, s))
	}
	out.Status, out.Undo = StatusDone, map[string]string{}
	return *out
}

func waitAliState(ctx context.Context, env *Env, c *aliyun.Client, s aliyun.Server, want string) bool {
	ok, _ := waitFor(ctx, env, 36, func() (bool, error) {
		now, found, err := c.Instance(ctx, s.Region, s.ID)
		return found && now.State == want, err
	})
	return ok
}

func aliStartAgain(ctx context.Context, env *Env, c *aliyun.Client, s aliyun.Server) string {
	if err := c.Power(ctx, s.Region, s.ID, "start"); err != nil {
		return "重新开机失败：" + err.Error() + "，请到阿里云控制台开机"
	}
	if !waitAliState(ctx, env, c, s, "RUNNING") {
		return "已经发出开机指令，但还没确认开好，请稍后到阿里云控制台查看"
	}
	return "已重新开机"
}

func snapName(name, id string) string {
	if name == "" || name == id {
		return id
	}
	return name + "（" + id + "）"
}
