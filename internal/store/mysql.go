package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// MySQL says where a MySQL (5.7 or later) or MariaDB (10.3 or later)
// database is. The web edition can keep its data there instead of in the
// SQLite file; the database must exist, and the account may create
// tables in it.
type MySQL struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
}

func (m MySQL) config() *mysql.Config {
	c := mysql.NewConfig()
	c.Net, c.Addr = "tcp", net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	if m.Port == 0 {
		c.Addr = net.JoinHostPort(m.Host, "3306")
	}
	c.DBName, c.User, c.Passwd = m.Database, m.User, m.Password
	c.Collation = "utf8mb4_bin" // compare text exactly, as SQLite does
	c.Timeout, c.ReadTimeout, c.WriteTimeout = 10*time.Second, 2*time.Minute, 2*time.Minute
	c.Params = map[string]string{"time_zone": "'+00:00'"}
	return c
}

// String says where the database is, without the password.
func (m MySQL) String() string {
	port := m.Port
	if port == 0 {
		port = 3306
	}
	return fmt.Sprintf("MySQL：%s@%s:%d/%s", m.User, m.Host, port, m.Database)
}

// OpenMySQL connects to a MySQL database and creates or updates the
// tables in it.
func OpenMySQL(m MySQL) (*Store, error) {
	c, err := mysql.NewConnector(m.config())
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(c)
	// A few connections, renewed before the server's idle timeout ends them.
	db.SetMaxOpenConns(4)
	db.SetConnMaxLifetime(3 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, MySQLError(err)
	}
	s := &Store{db: db, mysql: true, info: m.String()}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// CheckMySQL tries a MySQL database for the install wizard: that it can
// be reached and logged in to, and that the account may create tables.
// It says which server it is.
func CheckMySQL(ctx context.Context, m MySQL) (string, error) {
	c, err := mysql.NewConnector(m.config())
	if err != nil {
		return "", err
	}
	db := sql.OpenDB(c)
	defer db.Close()
	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return "", MySQLError(err)
	}
	if err := checkVersion(version); err != nil {
		return version, err
	}
	const probe = "miaopanel_install_check"
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+probe+" (id INT PRIMARY KEY) ENGINE=InnoDB"); err != nil {
		return version, fmt.Errorf("这个账号不能在库 %s 里建表：%w", m.Database, MySQLError(err))
	}
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+probe)
	return version, nil
}

// checkVersion refuses servers too old for utf8mb4 indexes and stored
// generated columns: MySQL before 5.7, MariaDB before 10.3.
func checkVersion(v string) error {
	num := v
	if i := strings.IndexFunc(num, func(r rune) bool { return (r < '0' || r > '9') && r != '.' }); i >= 0 {
		num = num[:i]
	}
	parts := strings.Split(num, ".")
	major, _ := strconv.Atoi(parts[0])
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	mariadb := strings.Contains(strings.ToLower(v), "mariadb")
	switch {
	case mariadb && (major < 10 || major == 10 && minor < 3):
		return fmt.Errorf("MariaDB %s 太旧了，需要 10.3 或更新的版本", v)
	case !mariadb && (major < 5 || major == 5 && minor < 7):
		return fmt.Errorf("MySQL %s 太旧了，需要 5.7 或更新的版本", v)
	}
	return nil
}

// MySQLError puts the usual reasons a MySQL connection fails in words.
func MySQLError(err error) error {
	var me *mysql.MySQLError
	var ne net.Error
	switch {
	case errors.As(err, &me) && me.Number == 1045:
		return errors.New("用户名或密码不对（MySQL 拒绝登录）")
	case errors.As(err, &me) && me.Number == 1049:
		return errors.New("没有这个数据库，请先在 MySQL（或 1Panel、宝塔的数据库页）里创建它")
	case errors.As(err, &me) && (me.Number == 1044 || me.Number == 1142):
		return errors.New("这个账号没有权限使用这个数据库")
	case errors.As(err, &ne) && ne.Timeout():
		return errors.New("连接超时：地址或端口不对，或者被防火墙挡住了")
	case strings.Contains(err.Error(), "connection refused"):
		return errors.New("连接被拒绝：这个地址和端口上没有 MySQL 在监听")
	case strings.Contains(err.Error(), "no such host"):
		return errors.New("找不到这个主机名")
	}
	return err
}

// openTestMySQL makes a new database on the server dsn names, for one
// test, and drops it when the store closes.
func openTestMySQL(dsn string) (*Store, error) {
	m, err := mysqlDSN(dsn)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	m.Database = "miaotest_" + hex.EncodeToString(b)
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	_, err = admin.Exec("CREATE DATABASE `" + m.Database + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin")
	admin.Close()
	if err != nil {
		return nil, err
	}
	s, err := OpenMySQL(m)
	if err != nil {
		return nil, err
	}
	s.drop = m.Database
	return s, nil
}

// mysqlDSN reads user:password@tcp(host:port)/database.
func mysqlDSN(dsn string) (MySQL, error) {
	c, err := mysql.ParseDSN(dsn)
	if err != nil {
		return MySQL{}, err
	}
	host, port, _ := net.SplitHostPort(c.Addr)
	p, _ := strconv.Atoi(port)
	return MySQL{Host: host, Port: p, Database: c.DBName, User: c.User, Password: c.Passwd}, nil
}

// mysqlSchema is schema and monitorSchema for MySQL: types with lengths
// where a column is a key, long text without defaults (MySQL 5.7 allows
// none), exact collation, and the reserved words key and undo quoted.
// Email and phone are unique only when set, through generated columns
// that are NULL when empty (MySQL has no partial indexes).
var mysqlSchema = []string{`
CREATE TABLE IF NOT EXISTS servers (
	id          BIGINT PRIMARY KEY AUTO_INCREMENT,
	name        VARCHAR(255) NOT NULL,
	host        VARCHAR(255) NOT NULL,
	port        INT NOT NULL DEFAULT 22,
	username    VARCHAR(255) NOT NULL,
	auth_kind   VARCHAR(16) NOT NULL,
	key_path    VARCHAR(1024) NOT NULL DEFAULT '',
	host_key    VARCHAR(255) NOT NULL DEFAULT '',
	adapter     VARCHAR(16) NOT NULL DEFAULT '',
	created_at  VARCHAR(40) NOT NULL,
	instance_id VARCHAR(128) NOT NULL DEFAULT '',
	region      VARCHAR(64) NOT NULL DEFAULT ''
)`, `
CREATE TABLE IF NOT EXISTS host_profiles (
	server_id    BIGINT PRIMARY KEY,
	raw          MEDIUMTEXT NOT NULL,
	collected_at VARCHAR(40) NOT NULL,
	FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE
)`, `
CREATE TABLE IF NOT EXISTS plans (
	id         BIGINT PRIMARY KEY AUTO_INCREMENT,
	server_id  BIGINT NOT NULL DEFAULT 0,
	title      TEXT NOT NULL,
	reason     MEDIUMTEXT NOT NULL,
	steps      MEDIUMTEXT NOT NULL,
	status     VARCHAR(32) NOT NULL,
	result     MEDIUMTEXT NOT NULL,
	created_at VARCHAR(40) NOT NULL
)`, `
CREATE TABLE IF NOT EXISTS audit_logs (
	id     BIGINT PRIMARY KEY AUTO_INCREMENT,
	at     VARCHAR(40) NOT NULL,
	actor  VARCHAR(64) NOT NULL,
	action VARCHAR(191) NOT NULL,
	target TEXT NOT NULL,
	detail TEXT NOT NULL,
	INDEX audit_logs_action (action, at)
)`, `
CREATE TABLE IF NOT EXISTS ai_usage (
	id            BIGINT PRIMARY KEY AUTO_INCREMENT,
	at            VARCHAR(40) NOT NULL,
	model         VARCHAR(191) NOT NULL,
	input_tokens  BIGINT NOT NULL,
	cached_tokens BIGINT NOT NULL DEFAULT 0,
	output_tokens BIGINT NOT NULL,
	cost          DOUBLE NOT NULL,
	currency      VARCHAR(16) NOT NULL
)`, `
CREATE TABLE IF NOT EXISTS exec_logs (
	id            BIGINT PRIMARY KEY AUTO_INCREMENT,
	started_at    VARCHAR(40) NOT NULL,
	finished_at   VARCHAR(40) NOT NULL DEFAULT '',
	server_id     BIGINT NOT NULL,
	server_name   VARCHAR(255) NOT NULL,
	adapter       VARCHAR(16) NOT NULL DEFAULT '',
	origin        VARCHAR(16) NOT NULL,
	kind          VARCHAR(16) NOT NULL,
	title         TEXT NOT NULL,
	note          MEDIUMTEXT NOT NULL,
	capability    VARCHAR(191) NOT NULL DEFAULT '',
	params        MEDIUMTEXT NOT NULL,
	via           VARCHAR(255) NOT NULL DEFAULT '',
	commands      MEDIUMTEXT NOT NULL,
	script_name   VARCHAR(255) NOT NULL DEFAULT '',
	script        MEDIUMTEXT NOT NULL,
	status        VARCHAR(32) NOT NULL,
	output        MEDIUMTEXT NOT NULL,
	` + "`undo`" + `        MEDIUMTEXT NOT NULL,
	reversible    TINYINT NOT NULL DEFAULT 0,
	backup_dir    VARCHAR(1024) NOT NULL DEFAULT '',
	rollback_file VARCHAR(1024) NOT NULL DEFAULT '',
	plan_id       BIGINT NOT NULL DEFAULT 0,
	step_idx      INT NOT NULL DEFAULT -1,
	undo_of       BIGINT NOT NULL DEFAULT 0,
	undone_by     BIGINT NOT NULL DEFAULT 0,
	INDEX exec_logs_server (server_id, id)
)`, `
CREATE TABLE IF NOT EXISTS conversations (
	id         VARCHAR(191) PRIMARY KEY,
	title      TEXT NOT NULL,
	created_at VARCHAR(40) NOT NULL,
	updated_at VARCHAR(40) NOT NULL
)`, `
CREATE TABLE IF NOT EXISTS chat_messages (
	id      BIGINT PRIMARY KEY AUTO_INCREMENT,
	conv_id VARCHAR(191) NOT NULL,
	at      VARCHAR(40) NOT NULL,
	role    VARCHAR(16) NOT NULL,
	text    MEDIUMTEXT NOT NULL,
	extra   MEDIUMTEXT NOT NULL,
	INDEX chat_messages_conv (conv_id, id),
	FOREIGN KEY (conv_id) REFERENCES conversations(id) ON DELETE CASCADE
)`, `
CREATE TABLE IF NOT EXISTS settings (
	` + "`key`" + ` VARCHAR(191) PRIMARY KEY,
	value MEDIUMTEXT NOT NULL
)`, `
CREATE TABLE IF NOT EXISTS users (
	id         BIGINT PRIMARY KEY AUTO_INCREMENT,
	name       VARCHAR(191) NOT NULL UNIQUE,
	password   VARCHAR(255) NOT NULL,
	totp       TINYINT NOT NULL DEFAULT 0,
	created_at VARCHAR(40) NOT NULL,
	changed_at VARCHAR(40) NOT NULL,
	email      VARCHAR(191) NOT NULL DEFAULT '',
	phone      VARCHAR(64) NOT NULL DEFAULT '',
	email_set  VARCHAR(191) GENERATED ALWAYS AS (NULLIF(email, '')) STORED,
	phone_set  VARCHAR(64) GENERATED ALWAYS AS (NULLIF(phone, '')) STORED,
	UNIQUE KEY users_email (email_set),
	UNIQUE KEY users_phone (phone_set)
)`, `
CREATE TABLE IF NOT EXISTS sessions (
	id         VARCHAR(191) PRIMARY KEY,
	user_id    BIGINT NOT NULL,
	created_at VARCHAR(40) NOT NULL,
	seen_at    VARCHAR(40) NOT NULL,
	ip         VARCHAR(64) NOT NULL DEFAULT '',
	ua         TEXT NOT NULL,
	FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
)`, `
CREATE TABLE IF NOT EXISTS uptime_checks (
	id     BIGINT PRIMARY KEY AUTO_INCREMENT,
	target VARCHAR(512) NOT NULL,
	at     VARCHAR(40) NOT NULL,
	ok     TINYINT NOT NULL,
	status INT NOT NULL DEFAULT 0,
	ms     INT NOT NULL DEFAULT 0,
	error  TEXT NOT NULL,
	INDEX uptime_checks_target (target(191), at),
	INDEX uptime_checks_at (at)
)`, `
CREATE TABLE IF NOT EXISTS server_metrics (
	id        BIGINT PRIMARY KEY AUTO_INCREMENT,
	server_id BIGINT NOT NULL,
	at        VARCHAR(40) NOT NULL,
	ok        TINYINT NOT NULL,
	cpu       DOUBLE NOT NULL DEFAULT 0,
	mem       DOUBLE NOT NULL DEFAULT 0,
	disk      DOUBLE NOT NULL DEFAULT 0,
	load1     DOUBLE NOT NULL DEFAULT 0,
	rx        DOUBLE NOT NULL DEFAULT 0,
	tx        DOUBLE NOT NULL DEFAULT 0,
	error     TEXT NOT NULL,
	INDEX server_metrics_server (server_id, at),
	INDEX server_metrics_at (at)
)`, `
CREATE TABLE IF NOT EXISTS incidents (
	id         BIGINT PRIMARY KEY AUTO_INCREMENT,
	kind       VARCHAR(16) NOT NULL,
	target     VARCHAR(512) NOT NULL,
	name       VARCHAR(512) NOT NULL DEFAULT '',
	started_at VARCHAR(40) NOT NULL,
	ended_at   VARCHAR(40) NOT NULL DEFAULT '',
	reason     TEXT NOT NULL,
	INDEX incidents_started (started_at),
	INDEX incidents_open (kind, target(191), ended_at)
)`}
