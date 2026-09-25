// Package tatx runs commands on Tencent Cloud instances through their
// automation agent (TAT, 腾讯云自动化助手), for servers Miao Panel does
// not log in to over SSH. Commands run as root.
//
// TAT has no stdin and returns at most 24KB of output, so each command is
// wrapped in a small script: it unpacks the command and its stdin, runs
// it, and packs exit code, stdout and stderr into one gzip+base64 blob.
// Blobs larger than one read stay in a private temporary directory and
// are fetched in pieces, then removed.
package tatx

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// chunk is how much of the packed output one command returns, safely
// under TAT's 24KB output limit.
const chunk = 18000

// maxContent is TAT's limit on a command's (base64) content.
const maxContent = 64 << 10

// Conn is a way into one instance through TAT.
type Conn struct {
	Cloud    *tencent.Client
	Region   string
	Instance string
	// Poll is how often to check on a running command; tests shorten it.
	Poll time.Duration
}

// Close does nothing: TAT keeps no connection open.
func (c *Conn) Close() error { return nil }

// RunScript pipes script into a shell; TAT already runs as root.
func (c *Conn) RunScript(ctx context.Context, _ string, script string, args []string, maxOut int) (sshx.Result, error) {
	return c.Run(ctx, sshx.ScriptCmd(args, false), script, maxOut)
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// wrapper is the script TAT runs for one command.
func wrapper(cmd, stdin string) string {
	return `set -u
d=$(mktemp -d /tmp/miaopanel.XXXXXXXX) || { echo "MIAOERR 无法创建临时目录"; exit 0; }
printf '%s' '` + b64(cmd) + `' | base64 -d > "$d/cmd"
printf '%s' '` + b64(stdin) + `' | base64 -d > "$d/in"
sh "$d/cmd" < "$d/in" > "$d/out" 2> "$d/err"
rc=$?
{ printf 'MIAO1 %s %s %s\n' "$rc" "$(wc -c < "$d/out" | tr -d ' ')" "$(wc -c < "$d/err" | tr -d ' ')"; cat "$d/out" "$d/err"; } > "$d/payload"
if command -v gzip >/dev/null 2>&1 && gzip -c "$d/payload" > "$d/z"; then z=gz; else cp "$d/payload" "$d/z"; z=raw; fi
base64 "$d/z" | tr -d '\n' > "$d/pack"
n=$(wc -c < "$d/pack" | tr -d ' ')
printf 'MIAOPACK %s %s %s\n' "$d" "$n" "$z"
head -c ` + strconv.Itoa(chunk) + ` "$d/pack"
if [ "$n" -le ` + strconv.Itoa(chunk) + ` ]; then rm -rf "$d"; fi
exit 0
`
}

var dirRe = regexp.MustCompile(`^/tmp/miaopanel\.[A-Za-z0-9]+$`)

// Run executes cmd on the instance with stdin.
func (c *Conn) Run(ctx context.Context, cmd, stdin string, maxOut int) (sshx.Result, error) {
	res := sshx.Result{Command: cmd}
	script := wrapper(cmd, stdin)
	if base64.StdEncoding.EncodedLen(len(script)) > maxContent {
		return res, errors.New("要执行的内容太长，超过了腾讯云自动化助手 64KB 的限制")
	}
	out, err := c.exec(ctx, script)
	if err != nil {
		return res, err
	}
	head, rest, _ := strings.Cut(out, "\n")
	if msg, ok := strings.CutPrefix(head, "MIAOERR "); ok {
		return res, errors.New(msg)
	}
	f := strings.Fields(head)
	if len(f) != 4 || f[0] != "MIAOPACK" || !dirRe.MatchString(f[1]) {
		return res, fmt.Errorf("自动化助手返回了无法识别的内容：%.200s", out)
	}
	total, _ := strconv.Atoi(f[2])
	packed := strings.TrimSpace(rest)
	if total > chunk {
		defer func() { _, _ = c.exec(context.WithoutCancel(ctx), "rm -rf '"+f[1]+"'\n") }()
		for len(packed) < total {
			more, err := c.exec(ctx, fmt.Sprintf("tail -c +%d '%s/pack' | head -c %d\n", len(packed)+1, f[1], chunk))
			if err != nil {
				return res, err
			}
			if more = strings.TrimSpace(more); more == "" {
				return res, errors.New("读取命令输出时中断了")
			}
			packed += more
		}
	}
	raw, err := base64.StdEncoding.DecodeString(packed)
	if err != nil {
		return res, fmt.Errorf("命令输出损坏：%w", err)
	}
	if f[3] == "gz" {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return res, fmt.Errorf("命令输出损坏：%w", err)
		}
		if raw, err = io.ReadAll(zr); err != nil {
			return res, fmt.Errorf("命令输出损坏：%w", err)
		}
	}
	line, body, _ := bytes.Cut(raw, []byte("\n"))
	h := strings.Fields(string(line))
	if len(h) != 4 || h[0] != "MIAO1" {
		return res, errors.New("命令输出损坏")
	}
	res.ExitCode, _ = strconv.Atoi(h[1])
	nOut, _ := strconv.Atoi(h[2])
	if nOut > len(body) {
		nOut = len(body)
	}
	stdout, stderr := string(body[:nOut]), string(body[nOut:])
	res.Stdout, res.Stderr = limit(stdout, maxOut, &res.Truncated), limit(stderr, maxOut, &res.Truncated)
	return res, nil
}

func limit(s string, max int, truncated *bool) string {
	if len(s) > max {
		*truncated = true
		return s[:max]
	}
	return s
}

// exec runs one script through TAT and waits for its output.
func (c *Conn) exec(ctx context.Context, script string) (string, error) {
	timeout := 600
	if d, ok := ctx.Deadline(); ok {
		timeout = int(time.Until(d).Seconds())
		if timeout < 30 {
			timeout = 30
		}
	}
	id, err := c.Cloud.RunShell(ctx, c.Region, c.Instance, script, timeout)
	if err != nil {
		return "", err
	}
	poll := c.Poll
	if poll <= 0 {
		poll = time.Second
	}
	for {
		t, err := c.Cloud.Invocation(ctx, c.Region, id)
		if err != nil {
			return "", err
		}
		if t.Finished() {
			switch t.Status {
			case "SUCCESS", "FAILED":
				if t.Dropped > 0 {
					return "", errors.New("自动化助手截断了命令输出")
				}
				return t.Output, nil
			case "TIMEOUT":
				return "", errors.New("命令执行超时")
			case "DELIVER_FAILED", "TASK_TIMEOUT":
				return "", fmt.Errorf("命令没能下发到服务器，请确认自动化助手在线（%s %s）", t.Status, t.ErrorInfo)
			}
			return "", fmt.Errorf("命令没有执行完（%s %s）", t.Status, t.ErrorInfo)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
	}
}
