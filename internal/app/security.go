package app

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// SecurityRequest creates a focused, confirmable security checklist for a server.
type SecurityRequest struct {
	Op          string `json:"op"` // backup, snapshot, firewall, ssh
	DatabaseApp string `json:"databaseApp"`
	HaloApp     string `json:"haloApp"`
	AdminCIDR   string `json:"adminCidr"`
}

func (a *App) ProposeServerSecurity(ctx context.Context, id int64, req SecurityRequest) (PlanView, error) {
	v, err := a.Profile(id)
	if err != nil {
		return PlanView{}, err
	}
	var title, reason string
	var steps []core.Step
	switch req.Op {
	case "backup":
		if v.Server.Adapter != "1panel" {
			return PlanView{}, userErr("应用与数据库备份需要先识别 1Panel")
		}
		panel, err := a.OnePanel(id)
		if err != nil {
			return PlanView{}, err
		}
		if !panel.HasKey {
			return PlanView{}, userErr("请先在服务器页面配置并测试 1Panel API")
		}
		cs, err := a.ServerCloud(ctx, id)
		if err != nil {
			return PlanView{}, err
		}
		if cs == nil {
			return PlanView{}, userErr("没有找到对应的腾讯云实例，无法创建整盘快照")
		}
		db, halo := strings.TrimSpace(req.DatabaseApp), strings.TrimSpace(req.HaloApp)
		if db == "" || halo == "" || strings.EqualFold(db, halo) {
			return PlanView{}, userErr("请填写不同的数据库应用名和 Halo 应用名")
		}
		apps := []string{}
		if v.Profile != nil {
			apps = v.Profile.Panel.Apps
		}
		containsApp := func(name string) bool {
			for _, app := range apps {
				_, installed, ok := strings.Cut(app, "/")
				if !ok {
					installed = app
				}
				if strings.EqualFold(installed, name) {
					return true
				}
			}
			return false
		}
		if !containsApp(db) || !containsApp(halo) {
			return PlanView{}, userErr("识别结果里没有找到数据库应用 %s 和 Halo 应用 %s；请先重新识别服务器，并填写 1Panel 中的实例名", db, halo)
		}
		title = "备份数据库、Halo 和系统盘"
		reason = "先在 1Panel 备份数据库与 Halo，再创建腾讯云整盘快照。请确认这两个名称是 1Panel 中的真实应用；备份文件和快照需要定期验证可恢复性。云服务器 CVM 快照可能计费。"
		steps = []core.Step{
			{Capability: "backup.create", Summary: "备份数据库应用 " + db + " 的全部数据库", Params: map[string]any{"app": db}},
			{Capability: "backup.create", Summary: "备份 Halo 应用 " + halo, Params: map[string]any{"app": halo}},
			{Capability: "cloud.snapshot.create", Summary: "给 " + cs.Name + " 创建整盘快照", Params: map[string]any{"instance": cs.ID, "region": cs.Region}},
		}
	case "snapshot":
		cs, err := a.ServerCloud(ctx, id)
		if err != nil {
			return PlanView{}, err
		}
		if cs == nil {
			return PlanView{}, userErr("没有找到对应的腾讯云实例")
		}
		title = "创建 " + cs.Name + " 的整盘快照"
		reason = "快照可在系统故障时恢复整台服务器。CVM 快照可能产生费用；创建后请在腾讯云控制台确认状态为可用。"
		steps = []core.Step{{Capability: "cloud.snapshot.create", Summary: "创建整盘快照", Params: map[string]any{"instance": cs.ID, "region": cs.Region}}}
	case "firewall":
		cs, err := a.ServerCloud(ctx, id)
		if err != nil {
			return PlanView{}, err
		}
		if cs == nil || cs.Kind != tencent.CVM || len(cs.Groups) != 1 {
			return PlanView{}, userErr("这项操作只支持绑定一个安全组的腾讯云 CVM；请先核对实例和安全组")
		}
		ip, cidr, err := net.ParseCIDR(strings.TrimSpace(req.AdminCIDR))
		if err != nil || ip.To4() == nil || cidr.String() == "0.0.0.0/0" {
			return PlanView{}, userErr("请填写你当前可用的 IPv4 管理网段，例如你的公网 IP/32")
		}
		c := a.tencentClient()
		if c == nil {
			return PlanView{}, userErr("请先配置腾讯云密钥")
		}
		rules, err := c.SecurityGroupIngress(ctx, cs.Region, cs.Groups[0])
		if err != nil {
			return PlanView{}, err
		}
		if _, err := actions.FirewallTightenRules(rules, v.Server.Port); err != nil {
			return PlanView{}, userErr("安全组 %s：%v", cs.Groups[0], err)
		}
		title = "收紧安全组 " + cs.Groups[0]
		reason = fmt.Sprintf("当前有 ALL ALL 0.0.0.0/0。执行时先添加 TCP 80/443 对外放行、TCP %d/13940/8090/8080 仅 %s 可访问，再删除宽泛规则。3306 不会对外放行。此安全组绑定的其他实例也会受影响；若站点从 8080/8090 回源或使用其他端口，访问可能中断，请先核对依赖。", v.Server.Port, cidr.String())
		steps = []core.Step{{Capability: "cloud.firewall.tighten", Summary: "替换全端口放行为按端口规则", Params: map[string]any{"instance": cs.ID, "region": cs.Region, "group": cs.Groups[0], "admin_cidr": cidr.String(), "ssh_port": v.Server.Port}}}
	case "ssh":
		if v.Server.AuthKind != "key" || v.Server.Username == "root" {
			return PlanView{}, userErr("请先用非 root 用户的 SSH 密钥添加并测试这台服务器，才能关闭密码和 root 登录")
		}
		_, conn, err := a.connect(ctx, id)
		if err != nil {
			return PlanView{}, friendlySSHError(err)
		}
		defer conn.Close()
		check, err := conn.Run(ctx, "sudo -n sh -c 'if [ -x /usr/sbin/sshd ]; then /usr/sbin/sshd -t; elif command -v sshd >/dev/null 2>&1; then sshd -t; else exit 1; fi'", "", 1024)
		if err != nil || check.ExitCode != 0 {
			return PlanView{}, userErr("密钥连接可用，但免密 sudo 或现有 SSH 配置检查未通过；请先修好后再试")
		}
		title = "关闭 SSH 密码和 root 直连"
		reason = "已使用非 root 密钥连接。执行时会检查 SSH 配置、备份修改前文件，设置 5 分钟自动恢复保险；修改后将用密钥重新建立连接，确认成功才取消保险。请确保该用户有免密 sudo。"
		steps = []core.Step{{Capability: "ssh.harden", Summary: "禁止密码认证及 root 直接登录", Params: map[string]any{"login_user": v.Server.Username}}}
	default:
		return PlanView{}, userErr("不支持的安全操作 %q", req.Op)
	}
	p, _, err := a.proposePlan(ctx, "user", id, title, reason, steps)
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}
