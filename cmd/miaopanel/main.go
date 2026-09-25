// Command miaopanel runs Miao Panel (喵面板) on the local machine: it
// starts a web UI on 127.0.0.1 and opens it in the default browser.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/api"
	"github.com/bocmiao/CloudConsoleWithAI/internal/app"
	"github.com/bocmiao/CloudConsoleWithAI/internal/config"
	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// preferredPort keeps the address stable between runs when it is free.
const preferredPort = 18765

func main() {
	port := flag.Int("port", preferredPort, "local port for the web UI (falls back to a random free port)")
	dataDir := flag.String("data", "", "data directory (default: the user config dir)")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser automatically")
	flag.Parse()
	setupConsole()

	if err := run(*port, *dataDir, !*noBrowser); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败：", err)
		if runtime.GOOS == "windows" {
			fmt.Println("按回车键退出……")
			fmt.Scanln()
		}
		os.Exit(1)
	}
}

func run(port int, dataDir string, openBrowser bool) error {
	dir, err := config.DataDir(dataDir)
	if err != nil {
		return err
	}
	st, err := store.Open(dir)
	if err != nil {
		return fmt.Errorf("打开数据库: %w", err)
	}
	defer st.Close()
	sec := secrets.Open(dir)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
	}
	boundPort := ln.Addr().(*net.TCPAddr).Port

	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return err
	}
	token := hex.EncodeToString(tok)
	srv := &http.Server{
		Handler:           api.New(app.New(st, sec), token, boundPort, version),
		ReadHeaderTimeout: 10 * time.Second,
	}
	url := api.LaunchURL(boundPort, token)

	fmt.Printf("Miao Panel（喵面板）%s 已启动\n\n", version)
	fmt.Printf("  浏览器地址：%s\n", url)
	fmt.Printf("  数据目录：  %s\n", dir)
	if sec.Kind() == "keychain" {
		fmt.Println("  密钥保存在系统钥匙串（Windows 凭据管理器）中")
	} else {
		fmt.Println("  注意：系统钥匙串不可用，密钥保存在数据目录的 secrets.json 中（仅当前用户可读）")
	}
	fmt.Println("\n关闭这个窗口即可退出 Miao Panel。")

	if openBrowser {
		if err := openURL(url); err != nil {
			fmt.Println("没能自动打开浏览器，请手动复制上面的地址打开。")
		}
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-sig:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Println(err)
		}
	}
	return nil
}

func openURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
