// Package sshx runs scripts on servers over SSH, pinning each server's
// host key on first use.
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyChanged means the server presented a different host key than
// the one recorded on first connect: possibly a reinstall, possibly an
// attack. The caller must not continue without the user's say-so.
var ErrHostKeyChanged = errors.New("server host key changed")

// Target says how to reach and log in to a server.
type Target struct {
	Host          string
	Port          int
	User          string
	Password      string // for password or keyboard-interactive login
	KeyPath       string // private key file; takes precedence over Password
	KeyPassphrase string
	// KnownHostKey is the SHA256 fingerprint recorded earlier; empty on
	// first connect, in which case the presented key is trusted and returned.
	KnownHostKey string
}

// Client is an open SSH connection.
type Client struct {
	conn *ssh.Client
	// HostKey is the fingerprint the server presented.
	HostKey string
}

// Dial connects and authenticates.
func Dial(ctx context.Context, t Target) (*Client, error) {
	auth, err := authMethods(t)
	if err != nil {
		return nil, err
	}
	var presented string
	cfg := &ssh.ClientConfig{
		User: t.User,
		Auth: auth,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			presented = ssh.FingerprintSHA256(key)
			if t.KnownHostKey != "" && t.KnownHostKey != presented {
				return fmt.Errorf("%w: recorded %s, now %s", ErrHostKeyChanged, t.KnownHostKey, presented)
			}
			return nil
		},
		Timeout: 15 * time.Second,
	}
	port := t.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(t.Host, strconv.Itoa(port))
	d := net.Dialer{Timeout: 15 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(deadline)
	}
	c, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
	if err != nil {
		nc.Close()
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	return &Client{conn: ssh.NewClient(c, chans, reqs), HostKey: presented}, nil
}

func authMethods(t Target) ([]ssh.AuthMethod, error) {
	if t.KeyPath != "" {
		pem, err := os.ReadFile(t.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read key file: %w", err)
		}
		var signer ssh.Signer
		if t.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(t.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
		}
		if err != nil {
			return nil, fmt.Errorf("parse key file: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	if t.Password == "" {
		return nil, errors.New("no password or key file configured")
	}
	answer := func(_, _ string, questions []string, _ []bool) ([]string, error) {
		out := make([]string, len(questions))
		for i := range out {
			out[i] = t.Password
		}
		return out, nil
	}
	return []ssh.AuthMethod{ssh.Password(t.Password), ssh.KeyboardInteractive(answer)}, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Dial opens a TCP connection from the server's side, e.g. to a panel that
// only listens on the server's loopback.
func (c *Client) Dial(network, addr string) (net.Conn, error) { return c.conn.Dial(network, addr) }

// Result is the outcome of one remote command.
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
}

// limitedBuffer keeps the first max bytes and drops the rest.
type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room < len(p) {
		l.truncated = true
		if room > 0 {
			l.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return l.buf.Write(p)
}

// Run executes cmd with stdin, keeping at most maxOut bytes of each stream.
// A non-zero exit status is reported in Result, not as an error.
func (c *Client) Run(ctx context.Context, cmd, stdin string, maxOut int) (Result, error) {
	sess, err := c.conn.NewSession()
	if err != nil {
		return Result{}, err
	}
	defer sess.Close()
	stdout := &limitedBuffer{max: maxOut}
	stderr := &limitedBuffer{max: maxOut}
	sess.Stdout, sess.Stderr = stdout, stderr
	sess.Stdin = strings.NewReader(stdin)
	if err := sess.Start(cmd); err != nil {
		return Result{}, err
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		_ = sess.Close()
		return Result{}, ctx.Err()
	case err = <-done:
	}
	res := Result{
		Stdout: stdout.buf.String(), Stderr: stderr.buf.String(),
		Truncated: stdout.truncated || stderr.truncated,
	}
	var exitErr *ssh.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitStatus()
	default:
		return res, err
	}
	return res, nil
}

// scriptCmd runs a script from stdin with bash when present, else sh.
// args must already be safe words (callers validate them).
func scriptCmd(args []string, sudo bool) string {
	cmd := `sh -c 'if command -v bash >/dev/null 2>&1; then exec bash -s -- "$@"; else exec sh -s -- "$@"; fi' miaopanel`
	if len(args) > 0 {
		cmd += " " + strings.Join(args, " ")
	}
	if sudo {
		cmd = "sudo -n " + cmd
	}
	return cmd
}

// RunScript pipes script into a shell on the server. Non-root users get
// passwordless sudo when it is available, and fall back to their own
// privileges otherwise.
func (c *Client) RunScript(ctx context.Context, user, script string, args []string, maxOut int) (Result, error) {
	if user != "root" {
		res, err := c.Run(ctx, scriptCmd(args, true), script, maxOut)
		if err == nil && !(res.ExitCode != 0 && strings.Contains(res.Stderr, "sudo")) {
			return res, nil
		}
	}
	return c.Run(ctx, scriptCmd(args, false), script, maxOut)
}
