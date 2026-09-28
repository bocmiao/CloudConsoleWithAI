package actions

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
)

// ---- MySQL / MariaDB databases in a 1Panel database app ----
//
// Creating a database makes its user too, with a random password that
// Miao Panel does not keep: 1Panel shows it on its 数据库 page. Removing
// one backs it up first.

var dbNameRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

func isMySQL(a onepanel.InstalledApp) bool {
	return a.AppKey == "mysql" || a.AppKey == "mariadb" || a.AppKey == "mysql-cluster"
}

func randomPassword() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, 20)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return string(b)
}

func mysqlDB(ctx context.Context, c *onepanel.Client, app onepanel.InstalledApp, name string) (onepanel.MySQLDB, bool, error) {
	list, err := c.MySQLDatabases(ctx, app.Name)
	if err != nil {
		return onepanel.MySQLDB{}, false, err
	}
	for _, d := range list {
		if d.Name == name {
			return d, true, nil
		}
	}
	return onepanel.MySQLDB{}, false, nil
}

// backupDB backs one database up and waits for the file.
func backupDB(ctx context.Context, env *Env, app onepanel.InstalledApp, name string, report func(string, ...any)) (string, error) {
	task := newTaskID()
	report("先备份数据库 %s……", name)
	if err := env.OnePanel.BackupNote(ctx, app.AppKey, app.Name, name, task, "Miao Panel 删除前备份"); err != nil {
		return "", err
	}
	rec, err := waitBackup(ctx, env, app.AppKey, app.Name, name, task)
	if err != nil {
		return "", err
	}
	return rec.FileDir + "/" + rec.FileName, nil
}

func applyMySQLDB(ctx context.Context, env *Env, create bool, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	app, err := findApp(ctx, c, v["app"])
	if err != nil {
		return refused(out, "%v", err)
	}
	if !isMySQL(app) {
		return refused(out, "%s 不是 MySQL 或 MariaDB 应用", app.Name)
	}
	d, found, err := mysqlDB(ctx, c, app, v["name"])
	if err != nil {
		return refused(out, "读取 %s 的数据库列表失败：%v", app.Name, err)
	}
	if create {
		if found {
			report("%s 里已经有数据库 %s（用户 %s），不需要新建", app.Name, d.Name, d.Username)
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
		user := v["user"]
		if user == "" {
			user = v["name"]
		}
		perm := v["access"]
		if perm == "" {
			perm = "%"
		}
		report("正在 %s 里新建数据库 %s 和用户 %s（字符集 utf8mb4）", app.Name, v["name"], user)
		if err := c.CreateMySQLDB(ctx, app.Name, v["name"], user, randomPassword(), perm, "Miao Panel 新建"); err != nil {
			return refused(out, "新建失败：%v", err)
		}
		out.Undo["app"], out.Undo["name"] = app.Name, v["name"]
		report("完成：数据库 %s 建好了，用户 %s。密码是随机生成的，没有记在 Miao Panel 里，在 1Panel「数据库」页面可以查看或修改", v["name"], user)
		out.Status = StatusDone
		return *out
	}
	if !found {
		report("%s 里没有数据库 %s，不需要删除", app.Name, v["name"])
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	file, err := backupDB(ctx, env, app, d.Name, report)
	if err != nil {
		return refused(out, "备份失败，没有删除：%v", err)
	}
	report("已备份到 %s", file)
	report("正在删除数据库 %s 和它的用户 %s", d.Name, d.Username)
	if err := c.DeleteMySQLDB(ctx, app.AppKey, app.Name, d.ID); err != nil {
		out.Status = StatusFailed
		out.logf("删除失败：%v", err)
		return *out
	}
	out.Result = map[string]string{"backup": file}
	report("完成：数据库已删除。备份在 1Panel「备份」里，需要时可以新建同名数据库后恢复")
	out.Status, out.Undo = StatusDone, map[string]string{}
	return *out
}

// undoMySQLCreate removes a database it made, backing it up first in case
// something was written to it since.
func undoMySQLCreate(ctx context.Context, env *Env, undo map[string]string) error {
	c := env.OnePanel
	app, err := findApp(ctx, c, undo["app"])
	if err != nil {
		return err
	}
	d, found, err := mysqlDB(ctx, c, app, undo["name"])
	if err != nil || !found {
		return err
	}
	if _, err := backupDB(ctx, env, app, d.Name, func(string, ...any) {}); err != nil {
		return fmt.Errorf("备份失败，没有删除：%w", err)
	}
	return c.DeleteMySQLDB(ctx, app.AppKey, app.Name, d.ID)
}

func init() {
	appParam := Param{Name: "app", Kind: "name", Required: true, Desc: "1Panel 里的 MySQL 或 MariaDB 应用名称（只有一个时写 mysql 也行）"}
	nameCheck := func(v map[string]string) error {
		for _, k := range []string{"name", "user"} {
			if s := v[k]; s != "" && !dbNameRe.MatchString(s) {
				return fmt.Errorf("%s 只能用字母、数字和下划线（最多 64 个），%q 不行", map[string]string{"name": "数据库名", "user": "用户名"}[k], s)
			}
		}
		return nil
	}
	register(&Capability{
		Name: "mysql.db.create", Title: "新建 MySQL 数据库", Risk: core.R1, Reversible: true,
		Params: []Param{appParam,
			{Name: "name", Kind: "name", Required: true, Desc: "数据库名：字母、数字和下划线"},
			{Name: "user", Kind: "name", Desc: "用户名，不填和数据库名一样；密码随机生成，在 1Panel「数据库」页面查看"},
			{Name: "access", Kind: "enum", Enum: []string{"%", "localhost"}, Default: "%", Desc: "%：容器里的应用和其他机器都能连（1Panel 的默认）；localhost：只能在 MySQL 容器里连"}},
		Impls: map[string]Impl{"1panel": {Via: "1Panel 接口", Panel: "mysql_db_create", Downtime: "不影响其他数据库",
			Undo: "先备份这个数据库，再删掉它和它的用户"}},
		Check: nameCheck,
	})
	register(&Capability{
		Name: "mysql.db.delete", Title: "删除 MySQL 数据库", Risk: core.R3,
		NoUndo: "删除前先在 1Panel 里备份这个数据库，需要时可以新建同名数据库后从备份恢复",
		Params: []Param{appParam, {Name: "name", Kind: "name", Required: true, Desc: "要删除的数据库名"}},
		Impls:  map[string]Impl{"1panel": {Via: "1Panel 接口", Panel: "mysql_db_delete", Downtime: "用这个数据库的网站或程序会立即出错"}},
		Check:  nameCheck,
	})
}
