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
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/bocmiao/CloudConsoleWithAI/internal/api"
	"github.com/bocmiao/CloudConsoleWithAI/internal/app"
	"github.com/bocmiao/CloudConsoleWithAI/internal/auth"
	"github.com/bocmiao/CloudConsoleWithAI/internal/config"
	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
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
	st, err := store.Open(dir)
	if err != nil {
		return fmt.Errorf("打开数据库: %w", err)
	}
	defer st.Close()
	sec := secrets.OpenFile(dir) // a server has no desktop keychain
	a := app.New(st, sec)
	a.CacheDir = filepath.Join(dir, "cache")
	au := auth.New(st, sec, dir)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: api.NewServer(a, au, version), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	scheme := "http"
	if *cert != "" {
		scheme = "https"
	}
	log.Printf("Miao Panel（喵面板）Web 版 %s 已启动：%s://%s ，数据目录 %s", version, scheme, ln.Addr(), dir)
	if host, _, _ := net.SplitHostPort(*listen); scheme == "http" && !isLoopback(host) {
		log.Printf("注意：正在用 HTTP 接受其他机器的连接，密码会明文传输。请在前面加一个 HTTPS 反向代理（1Panel、宝塔、Nginx、Caddy），或者用 --tls-cert/--tls-key")
	}
	code, err := au.SetupCode()
	if err != nil {
		return err
	}
	if code != "" {
		log.Printf("\n\n  ==== 第一次使用 ====\n  在浏览器打开 Miao Panel，用下面的初始化码创建管理员账号：\n\n      %s\n\n  （初始化码也保存在 %s，创建账号后自动失效）\n", code, filepath.Join(dir, "setup-code"))
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go a.KeepWarm(ctx, 20*time.Minute)
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
	st, err := store.Open(dir)
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
	if err := auth.ResetPassword(st, secrets.OpenFile(dir), name, pw); err != nil {
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
