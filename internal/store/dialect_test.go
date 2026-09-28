package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// These run on SQLite, and on MySQL too when MIAO_TEST_MYSQL is set.
func TestUpsertsAndExactText(t *testing.T) {
	st, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Settings replace, and "key" (a reserved word in MySQL) works.
	for _, v := range []string{"a", "b"} {
		if err := st.SetSetting("theme", v); err != nil {
			t.Fatal(err)
		}
	}
	if v, _ := st.Setting("theme"); v != "b" {
		t.Fatalf("setting = %q", v)
	}
	if v, _ := st.Setting("THEME"); v != "" {
		t.Fatalf("keys compare exactly, got %q", v)
	}

	sv, err := st.AddServer(Server{Name: "blog", Host: "203.0.113.5", Port: 22, Username: "root", AuthKind: "password"})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"one", "two"} {
		if err := st.SaveProfile(sv.ID, raw, "linux"); err != nil {
			t.Fatal(err)
		}
	}
	if raw, _, err := st.GetProfile(sv.ID); err != nil || raw != "two" {
		t.Fatalf("profile = %q %v", raw, err)
	}

	// Long text survives whole; undo (reserved in MySQL) is read back.
	long := strings.Repeat("输出", 100000)
	e, err := st.AddExec(ExecLog{ServerID: sv.ID, ServerName: "blog", Origin: "plan", Kind: ExecChange, Title: "x", Status: ExecRunning, Output: long, Undo: map[string]string{"a": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetExec(e.ID); err != nil || got.Output != long || got.Undo["a"] != "b" {
		t.Fatalf("exec = %d chars, undo %v, %v", len(got.Output), got.Undo, err)
	}

	// Account names are exact, and an email or phone belongs to one
	// account at most, while many accounts may have none.
	a, err := st.AddUser("admin", "h")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.AddUser("Admin", "h")
	if err != nil {
		t.Fatalf("names differing in case: %v", err)
	}
	if _, err := st.AddUser("admin", "h"); err == nil {
		t.Fatal("the same name twice")
	}
	if err := st.SetContact(a.ID, "email", "me@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetContact(b.ID, "email", "me@example.com"); err == nil {
		t.Fatal("one email on two accounts")
	}
	if u, err := st.UserByEmail("me@example.com"); err != nil || u.ID != a.ID {
		t.Fatalf("by email = %+v %v", u, err)
	}

	// Deleting a server takes its profile with it.
	if err := st.DeleteServer(sv.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.GetProfile(sv.ID); err == nil {
		t.Fatal("profile left behind")
	}
}

// Taken names, emails and phones come back as ErrDuplicate from both
// databases; long text fits as it does in SQLite.
func TestDuplicatesAndLongText(t *testing.T) {
	st, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := st.AddUser("alice", "h")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := st.AddUser("bob", "h")
	if _, err := st.AddUser("alice", "h"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("same name: %v", err)
	}
	long := strings.Repeat("m", 240) + "@example.com"
	if err := st.SetContact(a.ID, "email", long); err != nil {
		t.Fatal(err)
	}
	if err := st.SetContact(b.ID, "email", long); !errors.Is(err, ErrDuplicate) {
		t.Errorf("same email: %v", err)
	}
	// Empty is not taken: both can have none.
	if err := st.SetContact(a.ID, "phone", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetContact(b.ID, "phone", ""); err != nil {
		t.Errorf("both without a phone: %v", err)
	}

	name := strings.Repeat("服务器", 200)
	sv, err := st.AddServer(Server{Name: name, Host: "203.0.113.5", Port: 22, Username: "root", AuthKind: "password"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetServer(sv.ID); got.Name != name {
		t.Errorf("long server name cut to %d", len(got.Name))
	}
	target := "https://example.com/" + strings.Repeat("a", 3000)
	if err := st.AddUptimeCheck(UptimeCheck{Target: target, OK: true, Status: 200}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.OpenIncident("site", target, strings.Repeat("站", 600), "down"); err != nil {
		t.Fatal(err)
	}
}

// Conversations used in the same second come newest first on both.
func TestConversationOrder(t *testing.T) {
	st, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, id := range []string{"b", "a", "c"} {
		if _, err := st.AddConversation(id, id); err != nil {
			t.Fatal(err)
		}
	}
	list, err := st.ListConversations(10)
	if err != nil || len(list) != 3 || list[0].ID != "c" || list[1].ID != "a" || list[2].ID != "b" {
		t.Fatalf("order = %+v %v", list, err)
	}
}

func TestCheckMySQL(t *testing.T) {
	if checkVersion("5.6.51") == nil || checkVersion("10.2.44-MariaDB") == nil {
		t.Error("old servers accepted")
	}
	for _, v := range []string{"5.7.44-log", "8.0.39", "10.11.14-MariaDB-0ubuntu0.24.04.1", "11.4.2-MariaDB"} {
		if err := checkVersion(v); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, _, err := CheckMySQL(ctx, MySQL{Host: "127.0.0.1", Port: 1, Database: "x", User: "x"}); err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Errorf("nothing listening: %v", err)
	}
	dsn := os.Getenv("MIAO_TEST_MYSQL")
	if dsn == "" {
		t.Skip("MIAO_TEST_MYSQL not set")
	}
	st, err := openTestMySQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := testMySQL(t, dsn, st.drop)
	if v, missing, err := CheckMySQL(ctx, m); err != nil || v == "" || missing {
		t.Fatalf("check = %q %v %v", v, missing, err)
	}
	bad := m
	bad.Password += "x"
	if _, _, err := CheckMySQL(ctx, bad); err == nil || !strings.Contains(err.Error(), "密码") {
		t.Errorf("wrong password: %v", err)
	}
	// A database not made yet is found missing, then made when installing.
	fresh := m
	fresh.Database = st.drop + "_new"
	if _, missing, err := CheckMySQL(ctx, fresh); err != nil || !missing {
		t.Fatalf("missing database: %v %v", missing, err)
	}
	if _, err := OpenMySQL(fresh); err == nil || !strings.Contains(err.Error(), "没有这个数据库") {
		t.Errorf("open a missing database: %v", err)
	}
	for range 2 {
		if err := CreateMySQLDatabase(ctx, fresh); err != nil {
			t.Fatal(err)
		}
	}
	made, err := OpenMySQL(fresh)
	if err != nil {
		t.Fatal(err)
	}
	made.drop = fresh.Database
	made.Close()
	// Where says where, never the password.
	if want := fmt.Sprintf("MySQL：%s@%s:%d/%s", m.User, m.Host, m.Port, m.Database); st.Where() != want {
		t.Errorf("where = %q, want %q", st.Where(), want)
	}
}

func testMySQL(t *testing.T, dsn, db string) MySQL {
	t.Helper()
	c, err := mysqlDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	c.Database = db
	return c
}
