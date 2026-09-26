package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
)

// Terminals are interactive shells the user types into from the 终端
// page. They are the user's own hands on the server: the AI cannot use
// them, and what is typed is not recorded, only that a terminal was
// opened and closed.

// termBacklog is how much recent output a terminal keeps, so a page that
// attaches again (after a reload) can redraw it.
const termBacklog = 256 << 10

// termIdle closes a terminal no page has been attached to for this long.
var termIdle = 2 * time.Minute

// TerminalView describes an open terminal.
type TerminalView struct {
	ID         string `json:"id"`
	ServerID   int64  `json:"serverId"`
	ServerName string `json:"serverName"`
	StartedAt  string `json:"startedAt"`
	Ended      bool   `json:"ended"`
}

type terminal struct {
	view  TerminalView
	shell *sshx.Shell
	conn  io.Closer
	idle  time.Duration // termIdle when it was opened

	mu       sync.Mutex
	buf      []byte // the last termBacklog bytes of output
	total    int64  // bytes of output so far; buf ends here
	changed  chan struct{}
	ended    bool
	attached int
	detached time.Time
}

type terminals struct {
	mu   sync.Mutex
	open map[string]*terminal
}

// OpenTerminal starts a login shell on a server, cols x rows characters.
func (a *App) OpenTerminal(ctx context.Context, serverID int64, cols, rows int) (TerminalView, error) {
	if cols < 10 || rows < 3 || cols > 1000 || rows > 500 {
		cols, rows = 100, 30
	}
	sv, c, err := a.connect(ctx, serverID)
	if err != nil {
		return TerminalView{}, err
	}
	client, ok := c.(*sshx.Client)
	if !ok {
		c.Close()
		return TerminalView{}, userErr("这台服务器用腾讯云自动化助手连接，没有 SSH，不能打开终端。可以在腾讯云控制台用「OrcaTerm」登录，或者改用 SSH 方式添加这台服务器。")
	}
	sh, err := client.Shell(cols, rows)
	if err != nil {
		client.Close()
		return TerminalView{}, userErr("打开终端失败：%v", err)
	}
	t := &terminal{
		view:    TerminalView{ID: newID(), ServerID: sv.ID, ServerName: sv.Name, StartedAt: now()},
		shell:   sh,
		conn:    client,
		changed: make(chan struct{}),
		idle:    termIdle,
		// Counts as detached until a page attaches, so an abandoned open
		// is cleaned up too.
		detached: time.Now(),
	}
	a.terms.mu.Lock()
	if a.terms.open == nil {
		a.terms.open = map[string]*terminal{}
	}
	a.terms.open[t.view.ID] = t
	a.terms.mu.Unlock()
	_ = a.Store.Audit("user", "terminal.open", sv.Name, fmt.Sprintf("%d×%d", cols, rows))
	go t.pump()
	go a.reapTerminal(t)
	return t.view, nil
}

// pump reads the shell's output into the backlog until it ends.
func (t *terminal) pump() {
	chunk := make([]byte, 32<<10)
	for {
		n, err := t.shell.Output.Read(chunk)
		if n > 0 {
			t.append(chunk[:n])
		}
		if err != nil {
			break
		}
	}
	<-t.shell.Done()
	msg := "\r\n\x1b[90m[会话已结束]\x1b[0m\r\n"
	if err := t.shell.Err(); err != nil {
		var exit interface{ ExitStatus() int }
		if errors.As(err, &exit) {
			msg = fmt.Sprintf("\r\n\x1b[90m[会话已结束，退出码 %d]\x1b[0m\r\n", exit.ExitStatus())
		} else {
			msg = "\r\n\x1b[90m[连接已断开]\x1b[0m\r\n"
		}
	}
	t.append([]byte(msg))
	t.mu.Lock()
	t.ended = true
	t.view.Ended = true
	close(t.changed)
	t.changed = make(chan struct{})
	t.mu.Unlock()
	t.conn.Close()
}

func (t *terminal) append(p []byte) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - termBacklog; over > 0 {
		t.buf = append(t.buf[:0:0], t.buf[over:]...)
	}
	t.total += int64(len(p))
	close(t.changed)
	t.changed = make(chan struct{})
	t.mu.Unlock()
}

// reapTerminal closes a terminal once no page has been attached to it for
// its idle time, e.g. after the window was closed.
func (a *App) reapTerminal(t *terminal) {
	tick := time.NewTicker(max(t.idle/4, 10*time.Millisecond))
	defer tick.Stop()
	for range tick.C {
		t.mu.Lock()
		ended, idle := t.ended, t.attached == 0 && time.Since(t.detached) > t.idle
		t.mu.Unlock()
		if idle && !ended {
			_ = a.CloseTerminal(t.view.ID)
		}
		if ended || idle {
			a.terms.mu.Lock()
			delete(a.terms.open, t.view.ID)
			a.terms.mu.Unlock()
			return
		}
	}
}

func (a *App) terminal(id string) (*terminal, error) {
	a.terms.mu.Lock()
	defer a.terms.mu.Unlock()
	t := a.terms.open[id]
	if t == nil {
		return nil, userErr("这个终端已经关闭了")
	}
	return t, nil
}

// Terminals lists the open terminals, oldest first.
func (a *App) Terminals() []TerminalView {
	a.terms.mu.Lock()
	defer a.terms.mu.Unlock()
	out := []TerminalView{}
	for _, t := range a.terms.open {
		t.mu.Lock()
		out = append(out, t.view)
		t.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt < out[j].StartedAt || (out[i].StartedAt == out[j].StartedAt && out[i].ID < out[j].ID)
	})
	return out
}

// TerminalOutput streams a terminal's output to w: the recent backlog
// first, then everything new, until the shell ends or ctx is done. w is
// first called with nil once the terminal is found.
func (a *App) TerminalOutput(ctx context.Context, id string, w func([]byte) error) error {
	t, err := a.terminal(id)
	if err != nil {
		return err
	}
	if err := w(nil); err != nil {
		return err
	}
	t.mu.Lock()
	t.attached++
	pos := t.total - int64(len(t.buf))
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.attached--
		t.detached = time.Now()
		t.mu.Unlock()
	}()
	for {
		t.mu.Lock()
		start := t.total - int64(len(t.buf))
		if pos < start {
			pos = start // fell behind the backlog
		}
		data := append([]byte(nil), t.buf[pos-start:]...)
		pos = t.total
		ended, changed := t.ended, t.changed
		t.mu.Unlock()
		if len(data) > 0 {
			if err := w(data); err != nil {
				return err
			}
		}
		if ended {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil
		}
	}
}

// TerminalInput types into a terminal.
func (a *App) TerminalInput(id, data string) error {
	t, err := a.terminal(id)
	if err != nil {
		return err
	}
	if _, err := t.shell.Write([]byte(data)); err != nil {
		return userErr("终端已经断开")
	}
	return nil
}

// ResizeTerminal tells the server the terminal's new size.
func (a *App) ResizeTerminal(id string, cols, rows int) error {
	t, err := a.terminal(id)
	if err != nil {
		return err
	}
	if cols < 10 || rows < 3 || cols > 1000 || rows > 500 {
		return userErr("终端大小不对")
	}
	return t.shell.Resize(cols, rows)
}

// CloseTerminal ends a terminal's shell and connection.
func (a *App) CloseTerminal(id string) error {
	t, err := a.terminal(id)
	if err != nil {
		return err
	}
	a.terms.mu.Lock()
	delete(a.terms.open, id)
	a.terms.mu.Unlock()
	_ = t.shell.Close()
	_ = t.conn.Close()
	_ = a.Store.Audit("user", "terminal.close", t.view.ServerName, "")
	return nil
}
