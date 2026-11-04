package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/pkg/sftp"
)

// The AI reads a server's files the way the file manager shows them, with
// secrets blanked and the files that are nothing but secrets (password
// hashes, private keys, SSH keys) left out altogether.

const (
	aiFileBytes  = 1 << 20 // the most of one file read
	aiFileRunes  = 40000   // the most of it shown at once
	aiFileListed = 300     // entries of a folder shown
)

// secretFile says why a path is not shown to the AI, or "".
func secretFile(p string) string {
	base := strings.ToLower(path.Base(p))
	for _, seg := range strings.Split(p, "/") {
		if seg == ".ssh" || seg == ".gnupg" {
			return "SSH 和 GPG 的密钥目录"
		}
	}
	switch {
	case p == "/etc/shadow" || p == "/etc/gshadow" || p == "/etc/shadow-" || p == "/etc/gshadow-" || p == "/etc/security/opasswd":
		return "系统账号的密码"
	case strings.HasPrefix(base, "id_") && !strings.HasSuffix(base, ".pub"):
		return "SSH 私钥"
	case strings.HasSuffix(base, ".key") || strings.Contains(base, "privkey") || strings.Contains(base, "private_key") ||
		strings.Contains(base, "private-key") || base == "private.pem":
		return "私钥"
	case strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx") || strings.HasSuffix(base, ".jks") || strings.HasSuffix(base, ".keystore"):
		return "证书密钥库"
	case strings.HasPrefix(p, "/proc/") || strings.HasPrefix(p, "/sys/") || strings.HasPrefix(p, "/dev/"):
		return "系统的虚拟文件"
	}
	return ""
}

var redactRules = []struct {
	re   *regexp.Regexp
	with string
}{
	// The same as the checks run on the server (scripts/discover.sh).
	{regexp.MustCompile(`(?i)((pass(word|wd)?|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)[A-Za-z0-9_.-]*["']?[ \t]*[=:][ \t]*)[^\s,;&]+`), "${1}***"},
	{regexp.MustCompile(`(?i)(--pass(word)?[= ])\S+`), "${1}***"},
	{regexp.MustCompile(`(mysql(dump|admin)?[^|;\n]*\s-p)\S+`), "${1}***"},
	{regexp.MustCompile(`(://[^:/@\s]+:)[^@\s]+@`), "${1}***@"},
	{regexp.MustCompile(`(?i)(Bearer\s+)\S+`), "${1}***"},
	// WordPress and other PHP: define('DB_PASSWORD', '…'), the salts.
	{regexp.MustCompile(`(?i)(define\(\s*['"][A-Z0-9_]*(PASSWORD|KEY|SALT|SECRET|TOKEN)[A-Z0-9_]*['"]\s*,\s*['"])[^'"]*`), "${1}***"},
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), "（私钥，已隐藏）"},
}

// redactText blanks the secrets in a text.
func redactText(s string) string {
	for _, r := range redactRules {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}

// toolServerFiles lists a folder or reads a file on a server.
func (a *App) toolServerFiles(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		ServerID int64  `json:"server_id"`
		Path     string `json:"path"`
		FromLine int    `json:"from_line"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	p, err := cleanPath(arg.Path)
	if err != nil {
		return "", err
	}
	if why := secretFile(p); why != "" {
		return "", userErr("%s 是%s，不能读取", p, why)
	}
	var b strings.Builder
	err = a.withFiles(ctx, arg.ServerID, func(fc *fileConn, _ store.Server) error {
		real, err := resolveLinks(fc.sftp, p)
		if err != nil {
			return friendlyFileErr(err, p)
		}
		if why := secretFile(real); why != "" {
			return userErr("%s 指向 %s，是%s，不能读取", p, real, why)
		}
		st, err := fc.sftp.Stat(real)
		if err != nil {
			return friendlyFileErr(err, p)
		}
		if st.IsDir() {
			infos, err := fc.sftp.ReadDir(real)
			if err != nil {
				return friendlyFileErr(err, p)
			}
			fmt.Fprintf(&b, "%s（%d 项；类型权限 所有者 大小 修改时间 名字）：\n", p, len(infos))
			for i, fi := range infos {
				if i == aiFileListed {
					fmt.Fprintf(&b, "……还有 %d 项没有列出\n", len(infos)-aiFileListed)
					break
				}
				e := fc.entry(real, fi)
				name := e.Name
				if e.Dir {
					name += "/"
				}
				if e.Link != "" {
					name += " -> " + e.Link
				}
				fmt.Fprintf(&b, "%s %s:%s %s %s %s\n", e.Mode, e.Owner, e.Group, bytesText(e.Size), whenShort(e.ModTime), name)
			}
			return nil
		}
		if !st.Mode().IsRegular() {
			return userErr("%s 不是普通文件", p)
		}
		f, err := fc.sftp.Open(real)
		if err != nil {
			return friendlyFileErr(err, p)
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, aiFileBytes))
		if err != nil {
			return err
		}
		head := data[:min(len(data), 8192)]
		if bytes.IndexByte(head, 0) >= 0 || !utf8.Valid(head[:len(head)-trailingPartial(head)]) {
			fmt.Fprintf(&b, "%s 是二进制文件（%s），不能显示内容\n", p, bytesText(st.Size()))
			return nil
		}
		text := redactText(strings.ToValidUTF8(string(data), "�"))
		lines := strings.SplitAfter(text, "\n")
		if len(lines) > 1 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1] // the end of the last line, not one more
		}
		from := max(arg.FromLine, 1)
		if from > len(lines) {
			return userErr("%s 只有 %d 行", p, len(lines))
		}
		var body strings.Builder
		last, runes := from-1, 0
		for i := from - 1; i < len(lines); i++ {
			if runes += utf8.RuneCountInString(lines[i]); runes > aiFileRunes && i > from-1 {
				break
			}
			body.WriteString(clipText(lines[i], aiFileRunes))
			last = i + 1
		}
		fmt.Fprintf(&b, "%s（%s，%s 修改，权限 %03o；密码、密钥等已替换成 ***）第 %d~%d 行，共 %d 行", p, bytesText(st.Size()), whenShort(st.ModTime().UTC().Format("2006-01-02T15:04:05Z")),
			st.Mode().Perm(), from, last, len(lines))
		if int64(len(data)) < st.Size() {
			fmt.Fprintf(&b, "（文件太大，只读了前 %s）", bytesText(aiFileBytes))
		}
		b.WriteString("：\n" + body.String())
		if last < len(lines) {
			fmt.Fprintf(&b, "\n……后面还有，用 from_line=%d 接着读\n", last+1)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}

// resolveLinks follows every symbolic link in a path, one part at a
// time, so a link cannot lead to a file that is not to be read.
func resolveLinks(c *sftp.Client, p string) (string, error) {
	split := func(p string) []string {
		return strings.FieldsFunc(p, func(r rune) bool { return r == '/' })
	}
	parts, cur := split(p), "/"
	for hops := 0; len(parts) > 0; {
		next := path.Join(cur, parts[0])
		parts = parts[1:]
		fi, err := c.Lstat(next)
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		if hops++; hops > 40 {
			return "", userErr("%s 的链接太多层了", p)
		}
		target, err := c.ReadLink(next)
		if err != nil {
			return "", err
		}
		if !path.IsAbs(target) {
			target = path.Join(cur, target)
		}
		parts, cur = append(split(path.Clean(target)), parts...), "/"
	}
	return cur, nil
}

// trailingPartial is how many bytes at the end of b are the start of a
// character cut off by reading only the head.
func trailingPartial(b []byte) int {
	for n := 1; n <= 3 && n <= len(b); n++ {
		if utf8.RuneStart(b[len(b)-n]) {
			if !utf8.FullRune(b[len(b)-n:]) {
				return n
			}
			return 0
		}
	}
	return 0
}
