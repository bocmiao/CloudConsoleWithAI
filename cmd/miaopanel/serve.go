package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/bocmiao/CloudConsoleWithAI/internal/api"
	"github.com/bocmiao/CloudConsoleWithAI/internal/app"
	"github.com/bocmiao/CloudConsoleWithAI/internal/auth"
	"github.com/bocmiao/CloudConsoleWithAI/internal/config"
	"github.com/bocmiao/CloudConsoleWithAI/internal/dbconf"
	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/update"
)

// The web edition: "miaopanel serve" runs Miao Panel on a server, reached
// from a browser anywhere and logged in to with an account; "miaopanel
// reset-password" gets the owner back in from the server's command line.

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func serveMain(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", envOr("MIAO_LISTEN", "127.0.0.1:18765"), "address to listen on (env MIAO_LISTEN); use 0.0.0.0:18765 to accept connections from other machines")
	trustedProxies := fs.String("trusted-proxies", os.Getenv("MIAO_TRUSTED_PROXIES"), "comma-separated IPs or CIDRs of reverse proxies outside loopback (env MIAO_TRUSTED_PROXIES)")
	dataDir := fs.String("data", os.Getenv("MIAO_DATA"), "data directory (env MIAO_DATA; default: the user config dir)")
	cert := fs.String("tls-cert", os.Getenv("MIAO_TLS_CERT"), "certificate file for HTTPS (env MIAO_TLS_CERT)")
	key := fs.String("tls-key", os.Getenv("MIAO_TLS_KEY"), "private key file for HTTPS (env MIAO_TLS_KEY)")
	_ = fs.Parse(args)
	if (*cert == "") != (*key == "") {
		return errors.New("--tls-cert 和 --tls-key 要一起填")
	}

	dir, err := config.DataDir(*dataDir)
	if err != nil {
		return err
	}
	sec := secrets.OpenFile(dir) // a server has no desktop keychain
	update.CleanUp()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	// The server answers with the install wizard until a database is
	// chosen, then with Miao Panel itself: the handler is swapped in place.
	var current atomic.Pointer[http.Handler]
	set := func(h http.Handler) { current.Store(&h) }
	var opened []*store.Store
	var mu sync.Mutex
	start := func(st *store.Store) error {
		h, err := startWeb(ctx, st, sec, dir, *trustedProxies)
		if err != nil {
			return err
		}
		mu.Lock()
		opened = append(opened, st)
		mu.Unlock()
		set(h)
		log.Printf("数据保存在 %s", st.Where())
		return nil
	}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, st := range opened {
			st.Close()
		}
	}()
	st, err := dbconf.Open(dir, sec)
	switch {
	case errors.Is(err, dbconf.ErrNotInstalled):
		inst := api.NewInstaller(dir, sec, version, start)
		if err := inst.SetTrustedProxies(*trustedProxies); err != nil {
			return err
		}
		set(inst)
	case err != nil:
		return fmt.Errorf("打开数据库: %w", err)
	default:
		if err := start(st); err != nil {
			st.Close()
			return err
		}
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*current.Load()).ServeHTTP(w, r) }),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	scheme := "http"
	if *cert != "" {
		scheme = "https"
	}
	log.Printf("Miao Panel Web 版 %s 已启动：%s://%s ，数据目录 %s", version, scheme, ln.Addr(), dir)
	if host, _, _ := net.SplitHostPort(*listen); scheme == "http" && !isLoopback(host) {
		log.Printf("注意：正在用 HTTP 接受其他机器的连接，密码会明文传输。请在前面加一个 HTTPS 反向代理（1Panel、宝塔、Nginx、Caddy），或者用 --tls-cert/--tls-key")
	}
	if !dbconf.Installed(dir) || needsSetup(opened) {
		code, err := auth.InstallCode(dir)
		if err != nil {
			return err
		}
		log.Printf("\n\n  ==== 第一次使用 ====\n  在浏览器打开 Miao Panel，按安装向导操作，需要这个初始化码：\n\n      %s\n\n  （初始化码也保存在 %s，创建管理员账号后自动失效）\n", code, filepath.Join(dir, "setup-code"))
	}

	errCh := make(chan error, 1)
	go func() {
		if *cert != "" {
			errCh <- srv.ServeTLS(ln, *cert, *key)
		} else {
			errCh <- srv.Serve(ln)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-sig:
		log.Printf("正在退出……")
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}
	return nil
}

// needsSetup says whether the opened database has no account yet.
func needsSetup(opened []*store.Store) bool {
	for _, st := range opened {
		if n, err := st.CountUsers(); err == nil && n == 0 {
			return true
		}
	}
	return false
}

// startWeb makes Miao Panel's handler over a database and starts its
// background work.
func startWeb(ctx context.Context, st *store.Store, sec secrets.Store, dir, trustedProxies string) (http.Handler, error) {
	a := app.New(st, sec)
	a.CacheDir = filepath.Join(dir, "cache")
	a.Version, a.Restart = version, restartSelf
	au := auth.New(st, sec, dir)
	api.ConnectSenders(a, au) // login codes go out by the app's mail and SMS settings
	handler := api.NewServer(a, au, version)
	if err := handler.SetTrustedProxies(trustedProxies); err != nil {
		return nil, err
	}
	go a.KeepWarm(ctx, 20*time.Minute)
	go a.Monitor(ctx)
	go a.UpdateLoop(ctx)
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			_ = au.Prune()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return handler, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func resetPasswordMain(args []string) error {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	dataDir := fs.String("data", os.Getenv("MIAO_DATA"), "data directory (env MIAO_DATA)")
	user := fs.String("user", "", "account name (default: the existing account, or admin)")
	_ = fs.Parse(args)
	dir, err := config.DataDir(*dataDir)
	if err != nil {
		return err
	}
	sec := secrets.OpenFile(dir)
	st, err := dbconf.Open(dir, sec)
	if errors.Is(err, dbconf.ErrNotInstalled) {
		return errors.New("Miao Panel 还没有安装：启动后在浏览器里按安装向导创建管理员账号")
	}
	if err != nil {
		return fmt.Errorf("打开数据库: %w", err)
	}
	defer st.Close()
	name := strings.TrimSpace(*user)
	if name == "" {
		name = "admin"
		if u, err := st.GetUser(1); err == nil {
			name = u.Name
		}
	}
	pw, err := readPassword("为 " + name + " 设置新密码（至少 10 个字符）：")
	if err != nil {
		return err
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		again, err := readPassword("再输入一次：")
		if err != nil {
			return err
		}
		if again != pw {
			return errors.New("两次输入的密码不一样")
		}
	}
	if err := auth.ResetPassword(st, sec, name, pw); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dir, "setup-code")) // the account exists now
	fmt.Printf("已为 %s 设置新密码。两步验证已关闭，所有登录都已退出；如果之前输错太多次被锁，等 15 分钟或重启 Miao Panel。\n", name)
	return nil
}

// readPassword reads without echo from a terminal, or a line from a pipe
// (echo 'new password' | miaopanel reset-password).
func readPassword(prompt string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("没有读到新密码")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
