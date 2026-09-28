package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// The server's 应用 tab: restarting a service or a container, and the
// MySQL databases of a 1Panel server, each change a checklist.

// DBView is one MySQL database.
type DBView struct {
	Name      string `json:"name"`
	User      string `json:"user"`
	Access    string `json:"access"` // %, localhost or an IP
	CreatedAt string `json:"createdAt,omitempty"`
	Note      string `json:"note,omitempty"`
}

// DBAppView is a MySQL or MariaDB app and its databases.
type DBAppView struct {
	App       string   `json:"app"`
	Kind      string   `json:"kind"` // mysql, mariadb
	Version   string   `json:"version,omitempty"`
	Running   bool     `json:"running"`
	Databases []DBView `json:"databases"`
	Error     string   `json:"error,omitempty"`
}

// ServerDatabases lists the MySQL databases 1Panel manages on a server.
func (a *App) ServerDatabases(ctx context.Context, serverID int64) ([]DBAppView, error) {
	out := []DBAppView{}
	err := a.withPanel(ctx, serverID, func(_ store.Server, _ sshx.Conn, p *onepanel.Client) error {
		apps, err := p.InstalledApps(ctx)
		if err != nil {
			return userErr("读取 1Panel 应用失败：%v", err)
		}
		for _, ap := range apps {
			if ap.AppKey != "mysql" && ap.AppKey != "mariadb" && ap.AppKey != "mysql-cluster" {
				continue
			}
			v := DBAppView{App: ap.Name, Kind: ap.AppKey, Version: ap.Version, Running: strings.EqualFold(ap.Status, "running"), Databases: []DBView{}}
			list, err := p.MySQLDatabases(ctx, ap.Name)
			if err != nil {
				v.Error = err.Error()
			}
			for _, d := range list {
				dv := DBView{Name: d.Name, User: d.Username, Access: d.Permission, Note: d.Description}
				if !d.CreatedAt.IsZero() {
					dv.CreatedAt = d.CreatedAt.Format(time.RFC3339)
				}
				v.Databases = append(v.Databases, dv)
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}

// AppRequest is a change asked for on the 应用 tab.
type AppRequest struct {
	Op     string `json:"op"`   // service_restart, container_restart, db_create, db_delete, db_backup
	Name   string `json:"name"` // the service, container or database
	App    string `json:"app,omitempty"`
	User   string `json:"user,omitempty"`
	Access string `json:"access,omitempty"`
}

// ProposeServerApps turns a change on the 应用 tab into a checklist.
func (a *App) ProposeServerApps(ctx context.Context, serverID int64, req AppRequest) (PlanView, error) {
	sv, err := a.Store.GetServer(serverID)
	if err != nil {
		return PlanView{}, userErr("找不到这台服务器（编号 %d）", serverID)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return PlanView{}, userErr("请填写名称")
	}
	var capability, title, summary, reason string
	params := map[string]any{}
	switch req.Op {
	case "service_restart":
		capability, params["name"] = "service.restart", name
		title, summary = "重启服务："+name, "重启服务 "+name
		reason = "用 systemctl 重启这个服务，它会中断几秒。服务卡住、改了配置要生效时用。重启失败会显示原因。"
	case "container_restart":
		capability, params["name"] = "container.restart", name
		title, summary = "重启容器："+name, "重启 Docker 容器 "+name
		reason = "容器里的程序会中断几秒到几十秒（看程序启动多快）。数据在挂载的目录里，不会丢。"
	case "db_create":
		user := strings.TrimSpace(req.User)
		capability, params["app"], params["name"] = "mysql.db.create", req.App, name
		if user != "" {
			params["user"] = user
		} else {
			user = name
		}
		if req.Access != "" {
			params["access"] = req.Access
		}
		title, summary = "新建数据库："+name, fmt.Sprintf("在 %s 里新建数据库 %s 和用户 %s", req.App, name, user)
		reason = "字符集 utf8mb4。密码随机生成，不会记在 Miao Panel 里，在 1Panel「数据库」页面查看。可以一键撤销（先备份再删除这个数据库）。"
	case "db_delete":
		capability, params["app"], params["name"] = "mysql.db.delete", req.App, name
		title, summary = "删除数据库："+name, fmt.Sprintf("删除 %s 里的数据库 %s 和它的用户（先备份）", req.App, name)
		reason = "用这个数据库的网站或程序会立即出错。删除前会先在 1Panel 里备份，需要时可以新建同名数据库再恢复。这一步不能一键撤销。"
	case "db_backup":
		capability, params["app"], params["database"] = "backup.create", req.App, name
		title, summary = "备份数据库："+name, fmt.Sprintf("在 1Panel 里备份 %s 的数据库 %s", req.App, name)
		reason = "备份文件放在 1Panel 的备份目录，在 1Panel「数据库」页面可以恢复。不影响运行。"
	default:
		return PlanView{}, userErr("不支持的操作 %q", req.Op)
	}
	if _, err := actions.Resolve(capability, params, sv.Adapter); err != nil {
		return PlanView{}, userErr("%v", err)
	}
	p, _, err := a.proposePlan(ctx, "user", sv.ID, title, reason, []core.Step{{Capability: capability, Summary: summary, Params: params}})
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}
