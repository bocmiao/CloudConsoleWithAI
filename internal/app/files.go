package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// The file manager is the user's own hands on a server's files, like the
// terminal: browsing and editing go over SFTP on the SSH connection, and
// copying, moving, deleting and archives run the server's own commands
// (cp, mv, rm, tar, unzip). Nothing goes through the AI or the checklists;
// every change is written to the audit log. The system's own folders and
// the files that keep the server reachable cannot be deleted or moved.

// FileEntry is one file or folder.
type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Dir     bool   `json:"dir"`
	Link    string `json:"link,omitempty"` // where a symbolic link points
	Size    int64  `json:"size"`
	Mode    string `json:"mode"` // -rw-r--r--
	Perm    string `json:"perm"` // 644
	ModTime string `json:"modTime"`
	Owner   string `json:"owner"`
	Group   string `json:"group"`
}

// FileList is a folder's contents, folders first.
type FileList struct {
	Path    string      `json:"path"`
	Parent  string      `json:"parent,omitempty"`
	Home    string      `json:"home"`
	User    string      `json:"user"`
	Entries []FileEntry `json:"entries"`
}

// FileText is a text file opened for editing.
type FileText struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
	Perm    string `json:"perm"`
	Binary  bool   `json:"binary"` // not UTF-8 text: can only be downloaded
}

const (
	fileIdle    = 5 * time.Minute // an unused connection is closed after this
	maxEditSize = 5 << 20         // larger files are downloaded, not edited
	fileOpLimit = 30 * time.Minute
)

type fileConn struct {
	ssh    *sshx.Client
	sftp   *sftp.Client
	user   string
	home   string
	used   time.Time
	users  map[uint32]string
	groups map[uint32]string
}

func (fc *fileConn) close() {
	_ = fc.sftp.Close()
	_ = fc.ssh.Close()
}

type filePool struct {
	mu    sync.Mutex
	conns map[int64]*fileConn
	once  sync.Once
}

// fileConnFor reuses the server's file connection or opens one.
func (a *App) fileConnFor(ctx context.Context, id int64) (*fileConn, store.Server, error) {
	a.fpool.mu.Lock()
	if fc := a.fpool.conns[id]; fc != nil {
		fc.used = time.Now()
		a.fpool.mu.Unlock()
		sv, err := a.Store.GetServer(id)
		return fc, sv, err
	}
	a.fpool.mu.Unlock()
	sv, c, err := a.connect(ctx, id)
	if err != nil {
		return nil, sv, err
	}
	client, ok := c.(*sshx.Client)
	if !ok {
		c.Close()
		return nil, sv, userErr("这台服务器用腾讯云自动化助手连接，没有 SSH，不能管理文件。可以改用 SSH 方式添加这台服务器。")
	}
	sc, err := client.SFTP()
	if err != nil {
		client.Close()
		return nil, sv, userErr("打开文件管理失败，服务器可能关掉了 SFTP：%v", err)
	}
	fc := &fileConn{ssh: client, sftp: sc, user: sv.Username, used: time.Now()}
	if wd, err := sc.Getwd(); err == nil {
		fc.home = wd
	} else {
		fc.home = "/"
	}
	fc.users, fc.groups = readNames(sc, "/etc/passwd"), readNames(sc, "/etc/group")
	a.fpool.mu.Lock()
	if a.fpool.conns == nil {
		a.fpool.conns = map[int64]*fileConn{}
	}
	if old := a.fpool.conns[id]; old != nil { // opened meanwhile
		a.fpool.mu.Unlock()
		fc.close()
		return old, sv, nil
	}
	a.fpool.conns[id] = fc
	a.fpool.mu.Unlock()
	a.fpool.once.Do(func() { go a.closeIdleFiles() })
	return fc, sv, nil
}

func (a *App) closeIdleFiles() {
	for range time.Tick(time.Minute) {
		a.fpool.mu.Lock()
		for id, fc := range a.fpool.conns {
			if time.Since(fc.used) > fileIdle {
				fc.close()
				delete(a.fpool.conns, id)
			}
		}
		a.fpool.mu.Unlock()
	}
}

func (a *App) dropFileConn(id int64, fc *fileConn) {
	a.fpool.mu.Lock()
	if a.fpool.conns[id] == fc {
		delete(a.fpool.conns, id)
	}
	a.fpool.mu.Unlock()
	fc.close()
}

// withFiles runs op on the server's file connection, reconnecting once
// when the old connection turns out to be gone.
func (a *App) withFiles(ctx context.Context, id int64, op func(*fileConn, store.Server) error) error {
	for try := 0; ; try++ {
		fc, sv, err := a.fileConnFor(ctx, id)
		if err != nil {
			return err
		}
		err = op(fc, sv)
		if err != nil && try == 0 && lostConn(err) {
			a.dropFileConn(id, fc)
			continue
		}
		return err
	}
}

func lostConn(err error) bool {
	if errors.Is(err, sftp.ErrSSHFxConnectionLost) || errors.Is(err, io.EOF) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "use of closed network connection") || strings.Contains(s, "connection lost") || strings.Contains(s, "broken pipe")
}

// readNames maps ids to names from /etc/passwd or /etc/group.
func readNames(sc *sftp.Client, file string) map[uint32]string {
	out := map[uint32]string{}
	f, err := sc.Open(file)
	if err != nil {
		return out
	}
	defer f.Close()
	sc2 := bufio.NewScanner(io.LimitReader(f, 4<<20))
	for sc2.Scan() {
		parts := strings.Split(sc2.Text(), ":")
		if len(parts) >= 3 {
			if n, err := strconv.ParseUint(parts[2], 10, 32); err == nil {
				if _, ok := out[uint32(n)]; !ok {
					out[uint32(n)] = parts[0]
				}
			}
		}
	}
	return out
}

// cleanPath accepts only absolute paths.
func cleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || !strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return "", userErr("路径要以 / 开头：%s", p)
	}
	return path.Clean(p), nil
}

// validName is a file name the user typed: no slashes, not . or ..
func validName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\x00") || len(n) > 255 {
		return "", userErr("名字不对：%q（不能是空的，不能有 /）", n)
	}
	return n, nil
}

// shq quotes a word for the shell.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func shqAll(list []string) string {
	q := make([]string, len(list))
	for i, s := range list {
		q[i] = shq(s)
	}
	return strings.Join(q, " ")
}

// run runs a command on the server; a non-zero exit is an error with the
// command's own message.
func (fc *fileConn) run(ctx context.Context, cmd string) error {
	res, err := fc.ssh.Run(ctx, cmd, "", 64<<10)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(res.Stdout)
		}
		return userErr("执行失败：%s", clipText(msg, 400))
	}
	return nil
}

func (fc *fileConn) has(ctx context.Context, tool string) bool {
	res, err := fc.ssh.Run(ctx, "command -v "+shq(tool), "", 1024)
	return err == nil && res.ExitCode == 0
}

// protected paths can be opened and edited but not deleted, moved or
// renamed: the system's folders, and the files that keep it running and
// reachable.
var protectedPaths = map[string]bool{}

func init() {
	for _, p := range strings.Fields(`/ /bin /boot /dev /etc /home /lib /lib32 /lib64 /libx32 /media /mnt /opt /proc /root /run
		/sbin /snap /srv /sys /tmp /usr /var /usr/bin /usr/sbin /usr/lib /usr/lib64 /usr/local /usr/local/bin /usr/share
		/var/lib /var/log /var/run /var/spool /etc/passwd /etc/shadow /etc/group /etc/gshadow /etc/sudoers /etc/sudoers.d
		/etc/fstab /etc/hosts /etc/hostname /etc/resolv.conf /etc/ssh /etc/ssh/sshd_config /etc/systemd /etc/crontab
		/etc/profile /etc/bashrc /etc/bash.bashrc /etc/pam.d /etc/security /etc/network /etc/netplan /etc/sysconfig
		/root/.ssh /root/.ssh/authorized_keys /root/.bashrc /root/.profile
		/opt/1panel /opt/1panel/db /opt/1panel/apps /opt/1panel/www /usr/local/bin/1panel /usr/local/bin/1pctl /www /www/server`) {
		protectedPaths[p] = true
	}
}

var homeSSHRe = regexp.MustCompile(`^/home/[^/]+(/\.ssh(/authorized_keys)?)?$`)

func protected(p string) bool {
	if protectedPaths[p] || homeSSHRe.MatchString(p) {
		return true
	}
	for _, root := range []string{"/proc/", "/sys/", "/dev/", "/boot/"} {
		if strings.HasPrefix(p, root) {
			return true
		}
	}
	return false
}

func refuseProtected(paths []string, what string) error {
	for _, p := range paths {
		if protected(p) {
			return userErr("%s 是系统或登录要用的，不能%s", p, what)
		}
	}
	return nil
}

func (fc *fileConn) entry(dir string, fi os.FileInfo) FileEntry {
	p := path.Join(dir, fi.Name())
	e := FileEntry{Name: fi.Name(), Path: p, Dir: fi.IsDir(), Size: fi.Size(), ModTime: fi.ModTime().UTC().Format(time.RFC3339),
		Perm: fmt.Sprintf("%03o", fi.Mode().Perm())}
	kind := "-"
	switch m := fi.Mode(); {
	case m&os.ModeDir != 0:
		kind = "d"
	case m&os.ModeSymlink != 0:
		kind = "l"
	case m&os.ModeNamedPipe != 0:
		kind = "p"
	case m&os.ModeSocket != 0:
		kind = "s"
	case m&os.ModeCharDevice != 0:
		kind = "c"
	case m&os.ModeDevice != 0:
		kind = "b"
	}
	e.Mode = kind + fi.Mode().Perm().String()[1:]
	if st, ok := fi.Sys().(*sftp.FileStat); ok {
		e.Owner, e.Group = nameOr(fc.users, st.UID), nameOr(fc.groups, st.GID)
	}
	if kind == "l" {
		if t, err := fc.sftp.ReadLink(p); err == nil {
			e.Link = t
		}
		if st, err := fc.sftp.Stat(p); err == nil && st.IsDir() {
			e.Dir = true
		}
	}
	return e
}

func nameOr(m map[uint32]string, id uint32) string {
	if n, ok := m[id]; ok {
		return n
	}
	return strconv.FormatUint(uint64(id), 10)
}

func friendlyFileErr(err error, p string) error {
	var ue *UserError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ue):
		return err
	case errors.Is(err, os.ErrNotExist):
		return userErr("找不到 %s", p)
	case errors.Is(err, os.ErrPermission):
		return userErr("没有权限：%s（登录用的账号权限不够）", p)
	}
	return err
}

// ListFiles lists a folder; an empty path is the login user's home.
func (a *App) ListFiles(ctx context.Context, id int64, dir string) (FileList, error) {
	var out FileList
	err := a.withFiles(ctx, id, func(fc *fileConn, _ store.Server) error {
		d := dir
		if strings.TrimSpace(d) == "" {
			d = fc.home
		}
		d, err := cleanPath(d)
		if err != nil {
			return err
		}
		st, err := fc.sftp.Stat(d)
		if err != nil {
			return friendlyFileErr(err, d)
		}
		if !st.IsDir() {
			return userErr("%s 不是文件夹", d)
		}
		infos, err := fc.sftp.ReadDir(d)
		if err != nil {
			return friendlyFileErr(err, d)
		}
		out = FileList{Path: d, Home: fc.home, User: fc.user, Entries: make([]FileEntry, 0, len(infos))}
		if d != "/" {
			out.Parent = path.Dir(d)
		}
		for _, fi := range infos {
			out.Entries = append(out.Entries, fc.entry(d, fi))
		}
		sort.Slice(out.Entries, func(i, j int) bool {
			x, y := out.Entries[i], out.Entries[j]
			if x.Dir != y.Dir {
				return x.Dir
			}
			return strings.ToLower(x.Name) < strings.ToLower(y.Name)
		})
		return nil
	})
	return out, err
}

// ReadFileText opens a text file for editing.
func (a *App) ReadFileText(ctx context.Context, id int64, p string) (FileText, error) {
	var out FileText
	p, err := cleanPath(p)
	if err != nil {
		return out, err
	}
	err = a.withFiles(ctx, id, func(fc *fileConn, _ store.Server) error {
		st, err := fc.sftp.Stat(p)
		if err != nil {
			return friendlyFileErr(err, p)
		}
		if st.IsDir() {
			return userErr("%s 是文件夹", p)
		}
		if !st.Mode().IsRegular() { // a pipe or a device would never end
			return userErr("%s 不是普通文件，不能打开", path.Base(p))
		}
		out = FileText{Path: p, Size: st.Size(), ModTime: st.ModTime().UTC().Format(time.RFC3339), Perm: fmt.Sprintf("%03o", st.Mode().Perm())}
		if st.Size() > maxEditSize {
			return userErr("%s 有 %s，太大了，不能在这里编辑，可以下载后再改", path.Base(p), bytesText(st.Size()))
		}
		f, err := fc.sftp.Open(p)
		if err != nil {
			return friendlyFileErr(err, p)
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxEditSize+1))
		if err != nil {
			return err
		}
		head := data
		if len(head) > 8192 {
			head = head[:8192]
		}
		if bytes.IndexByte(head, 0) >= 0 || !utf8.Valid(data) {
			out.Binary = true
			return nil
		}
		out.Content = string(data)
		return nil
	})
	return out, err
}

// WriteFileText saves a text file. expect is the modification time the
// page read; when the file changed since, it refuses unless force.
func (a *App) WriteFileText(ctx context.Context, id int64, p, content, expect string, force bool) (FileText, error) {
	p, err := cleanPath(p)
	if err != nil {
		return FileText{}, err
	}
	if len(content) > maxEditSize {
		return FileText{}, userErr("内容太长了")
	}
	var server string
	err = a.withFiles(ctx, id, func(fc *fileConn, sv store.Server) error {
		server = sv.Name
		mode := os.FileMode(0o644)
		if st, err := fc.sftp.Stat(p); err == nil {
			if st.IsDir() {
				return userErr("%s 是文件夹", p)
			}
			if at := st.ModTime().UTC().Format(time.RFC3339); !force && expect != "" && at != expect {
				return &UserError{Msg: fmt.Sprintf("%s 在你打开之后被改过了（%s），保存会覆盖别人的修改。", path.Base(p), st.ModTime().Local().Format("01-02 15:04:05")), Code: "conflict"}
			}
			mode = st.Mode().Perm()
		}
		return fc.writeAtomic(p, strings.NewReader(content), mode)
	})
	if err != nil {
		return FileText{}, err
	}
	_ = a.Store.Audit("user", "files.edit", server, fmt.Sprintf("%s（%s）", p, bytesText(int64(len(content)))))
	return a.ReadFileText(ctx, id, p)
}

// writeAtomic writes to a temporary file next to p and renames it over p,
// keeping p's owner when possible, so a failed write leaves p as it was.
func (fc *fileConn) writeAtomic(p string, r io.Reader, mode os.FileMode) error {
	// A symbolic link (sites-enabled/x → ../sites-available/x) stays a
	// link: the file it points to is written.
	for i := 0; i < 40; i++ {
		st, err := fc.sftp.Lstat(p)
		if err != nil || st.Mode()&os.ModeSymlink == 0 {
			break
		}
		t, err := fc.sftp.ReadLink(p)
		if err != nil {
			break
		}
		if !path.IsAbs(t) {
			t = path.Join(path.Dir(p), t)
		}
		p = path.Clean(t)
	}
	tmp := path.Join(path.Dir(p), "."+path.Base(p)+".miao-tmp")
	f, err := fc.sftp.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return friendlyFileErr(err, path.Dir(p))
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		_ = fc.sftp.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = fc.sftp.Remove(tmp)
		return err
	}
	_ = fc.sftp.Chmod(tmp, mode)
	if st, err := fc.sftp.Stat(p); err == nil {
		if fs, ok := st.Sys().(*sftp.FileStat); ok {
			_ = fc.sftp.Chown(tmp, int(fs.UID), int(fs.GID))
		}
	} else {
		fc.ownLikeParent(tmp)
	}
	if err := fc.sftp.PosixRename(tmp, p); err != nil {
		if err2 := fc.sftp.Rename(tmp, p); err2 != nil {
			_ = fc.sftp.Remove(tmp)
			return friendlyFileErr(err, p)
		}
	}
	return nil
}

// UploadFile writes one uploaded file into dir. With overwrite false an
// existing file is not replaced.
func (a *App) UploadFile(ctx context.Context, id int64, dir, name string, r io.Reader, overwrite bool) (FileEntry, error) {
	var out FileEntry
	dir, err := cleanPath(dir)
	if err != nil {
		return out, err
	}
	if name, err = validName(path.Base(strings.ReplaceAll(name, `\`, "/"))); err != nil {
		return out, err
	}
	p := path.Join(dir, name)
	var server string
	err = a.withFiles(ctx, id, func(fc *fileConn, sv store.Server) error {
		server = sv.Name
		mode := os.FileMode(0o644)
		if st, err := fc.sftp.Lstat(p); err == nil {
			if st.IsDir() || !overwrite {
				return &UserError{Msg: fmt.Sprintf("%s 已经有同名的%s", dir, map[bool]string{true: "文件夹", false: "文件"}[st.IsDir()]), Code: "conflict"}
			}
			mode = st.Mode().Perm()
		}
		if err := fc.writeAtomic(p, r, mode); err != nil {
			return err
		}
		st, err := fc.sftp.Lstat(p)
		if err != nil {
			return err
		}
		out = fc.entry(dir, st)
		return nil
	})
	if err != nil {
		return out, err
	}
	_ = a.Store.Audit("user", "files.upload", server, fmt.Sprintf("%s（%s）", p, bytesText(out.Size)))
	return out, nil
}

// DownloadFile writes a file, or a folder packed as .tar.gz, to w; it
// returns the name to save it as. start is called before the first byte.
func (a *App) DownloadFile(ctx context.Context, id int64, p string, start func(name string, size int64), w io.Writer) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	return a.withFiles(ctx, id, func(fc *fileConn, _ store.Server) error {
		st, err := fc.sftp.Stat(p)
		if err != nil {
			return friendlyFileErr(err, p)
		}
		if st.IsDir() {
			if p == "/" {
				return userErr("不能下载整个根目录")
			}
			start(path.Base(p)+".tar.gz", -1)
			return fc.ssh.Stream(ctx, "tar -czf - -C "+shq(path.Dir(p))+" -- "+shq(path.Base(p)), w)
		}
		if !st.Mode().IsRegular() {
			return userErr("%s 不是普通文件，不能下载", p)
		}
		f, err := fc.sftp.Open(p)
		if err != nil {
			return friendlyFileErr(err, p)
		}
		defer f.Close()
		start(path.Base(p), st.Size())
		_, err = io.Copy(w, f)
		return err
	})
}

// FileOp is a change the page asks for.
type FileOp struct {
	Op        string   `json:"op"` // mkdir, touch, rename, delete, copy, move, extract, compress, chmod, chown
	Paths     []string `json:"paths"`
	Dir       string   `json:"dir"`       // where: the folder for mkdir/touch/copy/move/extract/compress
	Name      string   `json:"name"`      // new name for mkdir, touch, rename, compress
	Conflict  string   `json:"conflict"`  // copy/move onto existing names: "" asks, overwrite, rename, skip
	Format    string   `json:"format"`    // compress: zip or tar.gz
	Mode      string   `json:"mode"`      // chmod: 755
	Owner     string   `json:"owner"`     // chown: www-data or www-data:www-data
	Recursive bool     `json:"recursive"` // chmod/chown into folders
}

// FileOpResult says what was done.
type FileOpResult struct {
	Done    string   `json:"done"`
	Skipped []string `json:"skipped,omitempty"`
}

var (
	modeRe  = regexp.MustCompile(`^[0-7]{3,4}$`)
	ownerRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(:[A-Za-z0-9_][A-Za-z0-9_.-]*)?$`)
)

// FileChange applies one change.
func (a *App) FileChange(ctx context.Context, id int64, op FileOp) (FileOpResult, error) {
	ctx, cancel := context.WithTimeout(ctx, fileOpLimit)
	defer cancel()
	var paths []string
	for _, p := range op.Paths {
		c, err := cleanPath(p)
		if err != nil {
			return FileOpResult{}, err
		}
		paths = append(paths, c)
	}
	dir := ""
	if op.Dir != "" {
		d, err := cleanPath(op.Dir)
		if err != nil {
			return FileOpResult{}, err
		}
		dir = d
	}
	var res FileOpResult
	var server, detail string
	err := a.withFiles(ctx, id, func(fc *fileConn, sv store.Server) error {
		server = sv.Name
		var err error
		res, detail, err = fc.change(ctx, op, paths, dir)
		return err
	})
	if err != nil {
		return res, err
	}
	_ = a.Store.Audit("user", "files."+op.Op, server, detail)
	return res, nil
}

func (fc *fileConn) change(ctx context.Context, op FileOp, paths []string, dir string) (FileOpResult, string, error) {
	need := func(n int) error {
		if len(paths) < n {
			return userErr("没有选择文件")
		}
		return nil
	}
	switch op.Op {
	case "mkdir", "touch":
		name, err := validName(op.Name)
		if err != nil {
			return FileOpResult{}, "", err
		}
		if dir == "" {
			return FileOpResult{}, "", userErr("没有说在哪个文件夹")
		}
		p := path.Join(dir, name)
		if _, err := fc.sftp.Lstat(p); err == nil {
			return FileOpResult{}, "", userErr("%s 已经存在", name)
		}
		if op.Op == "mkdir" {
			err = fc.sftp.Mkdir(p)
		} else {
			var f *sftp.File
			if f, err = fc.sftp.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL); err == nil {
				err = f.Close()
			}
		}
		if err != nil {
			return FileOpResult{}, "", friendlyFileErr(err, dir)
		}
		fc.ownLikeParent(p)
		return FileOpResult{Done: "已新建 " + name}, p, nil

	case "rename":
		if err := need(1); err != nil {
			return FileOpResult{}, "", err
		}
		name, err := validName(op.Name)
		if err != nil {
			return FileOpResult{}, "", err
		}
		from := paths[0]
		if err := refuseProtected([]string{from}, "改名"); err != nil {
			return FileOpResult{}, "", err
		}
		to := path.Join(path.Dir(from), name)
		if to == from {
			return FileOpResult{Done: "名字没变"}, from, nil
		}
		if _, err := fc.sftp.Lstat(to); err == nil {
			return FileOpResult{}, "", &UserError{Msg: name + " 已经存在", Code: "conflict"}
		}
		if err := fc.sftp.Rename(from, to); err != nil {
			return FileOpResult{}, "", friendlyFileErr(err, from)
		}
		return FileOpResult{Done: "已改名为 " + name}, from + " → " + to, nil

	case "delete":
		if err := need(1); err != nil {
			return FileOpResult{}, "", err
		}
		if err := refuseProtected(paths, "删除"); err != nil {
			return FileOpResult{}, "", err
		}
		if err := fc.run(ctx, "rm -rf -- "+shqAll(paths)); err != nil {
			return FileOpResult{}, "", err
		}
		return FileOpResult{Done: fmt.Sprintf("已删除 %d 项", len(paths))}, strings.Join(paths, "、"), nil

	case "copy", "move":
		if err := need(1); err != nil {
			return FileOpResult{}, "", err
		}
		if dir == "" {
			return FileOpResult{}, "", userErr("没有说粘贴到哪个文件夹")
		}
		if op.Op == "move" {
			if err := refuseProtected(paths, "移动"); err != nil {
				return FileOpResult{}, "", err
			}
		}
		return fc.copyMove(ctx, op, paths, dir)

	case "extract":
		if err := need(1); err != nil {
			return FileOpResult{}, "", err
		}
		return fc.extract(ctx, paths[0], dir)

	case "compress":
		if err := need(1); err != nil {
			return FileOpResult{}, "", err
		}
		return fc.compress(ctx, op, paths)

	case "chmod":
		if err := need(1); err != nil {
			return FileOpResult{}, "", err
		}
		if !modeRe.MatchString(op.Mode) {
			return FileOpResult{}, "", userErr("权限要写成 755 这样的三位数字")
		}
		for _, p := range paths {
			if protected(p) && op.Recursive {
				return FileOpResult{}, "", userErr("不能给 %s 整个文件夹改权限", p)
			}
		}
		cmd := "chmod "
		if op.Recursive {
			cmd += "-R "
		}
		if err := fc.run(ctx, cmd+op.Mode+" -- "+shqAll(paths)); err != nil {
			return FileOpResult{}, "", err
		}
		return FileOpResult{Done: "权限已改为 " + op.Mode}, op.Mode + " " + strings.Join(paths, "、"), nil

	case "chown":
		if err := need(1); err != nil {
			return FileOpResult{}, "", err
		}
		if !ownerRe.MatchString(op.Owner) {
			return FileOpResult{}, "", userErr("所有者要写成 www 或 www:www 这样")
		}
		for _, p := range paths {
			if protected(p) {
				return FileOpResult{}, "", userErr("不能改 %s 的所有者", p)
			}
		}
		cmd := "chown "
		if op.Recursive {
			cmd += "-R "
		}
		if err := fc.run(ctx, cmd+shq(op.Owner)+" -- "+shqAll(paths)); err != nil {
			return FileOpResult{}, "", err
		}
		return FileOpResult{Done: "所有者已改为 " + op.Owner}, op.Owner + " " + strings.Join(paths, "、"), nil
	}
	return FileOpResult{}, "", userErr("不认识的操作：%s", op.Op)
}

// ownLikeParent gives a new file or folder the owner of the folder it is
// in, so what is uploaded or made in a website's folder belongs to the
// website's user rather than to root. Only root can change owners; for
// anyone else nothing happens.
func (fc *fileConn) ownLikeParent(p string) {
	parent, err := fc.sftp.Stat(path.Dir(p))
	if err != nil {
		return
	}
	ps, ok := parent.Sys().(*sftp.FileStat)
	if !ok {
		return
	}
	st, err := fc.sftp.Lstat(p)
	if err != nil {
		return
	}
	if s, ok := st.Sys().(*sftp.FileStat); ok && (s.UID != ps.UID || s.GID != ps.GID) {
		_ = fc.sftp.Chown(p, int(ps.UID), int(ps.GID))
	}
}

// freeName finds "name (2).ext" style names not yet in dir.
func (fc *fileConn) freeName(dir, name string) string {
	ext := path.Ext(name)
	if strings.HasSuffix(name, ".tar.gz") {
		ext = ".tar.gz"
	}
	stem := strings.TrimSuffix(name, ext)
	if stem == "" { // .bashrc
		stem, ext = name, ""
	}
	for i := 2; i < 1000; i++ {
		n := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, err := fc.sftp.Lstat(path.Join(dir, n)); err != nil {
			return n
		}
	}
	return fmt.Sprintf("%s (%d)%s", stem, time.Now().Unix(), ext)
}

func (fc *fileConn) copyMove(ctx context.Context, op FileOp, paths []string, dir string) (FileOpResult, string, error) {
	st, err := fc.sftp.Stat(dir)
	if err != nil || !st.IsDir() {
		return FileOpResult{}, "", userErr("%s 不是文件夹", dir)
	}
	var conflicts []string
	for _, p := range paths {
		if dir == p || strings.HasPrefix(dir+"/", p+"/") {
			return FileOpResult{}, "", userErr("不能把 %s 放进它自己里面", path.Base(p))
		}
		if _, err := fc.sftp.Lstat(path.Join(dir, path.Base(p))); err == nil {
			conflicts = append(conflicts, path.Base(p))
		}
	}
	if len(conflicts) > 0 && op.Conflict == "" {
		return FileOpResult{}, "", &UserError{Msg: fmt.Sprintf("%s 里已经有 %s", dir, strings.Join(conflicts, "、")), Code: "conflict"}
	}
	verb := map[string]string{"copy": "复制", "move": "移动"}[op.Op]
	var res FileOpResult
	done := 0
	var detail []string
	for _, p := range paths {
		target := path.Join(dir, path.Base(p))
		if target == p && op.Op == "move" {
			continue // already there
		}
		if _, err := fc.sftp.Lstat(target); err == nil {
			switch op.Conflict {
			case "skip":
				res.Skipped = append(res.Skipped, path.Base(p))
				continue
			case "rename":
				target = path.Join(dir, fc.freeName(dir, path.Base(p)))
			case "overwrite":
				if target == p {
					continue
				}
				if protected(target) {
					return res, "", userErr("%s 是系统或登录要用的，不能覆盖", target)
				}
				if err := fc.run(ctx, "rm -rf -- "+shq(target)); err != nil {
					return res, "", err
				}
			default:
				return res, "", userErr("不认识的处理方式：%s", op.Conflict)
			}
		}
		cmd := "cp -a -- " + shq(p) + " " + shq(target)
		if op.Op == "move" {
			cmd = "mv -- " + shq(p) + " " + shq(target)
		}
		if err := fc.run(ctx, cmd); err != nil {
			if done > 0 {
				return res, "", userErr("已%s %d 项，%s 时出错：%v", verb, done, path.Base(p), err)
			}
			return res, "", err
		}
		done++
		detail = append(detail, p+" → "+target)
	}
	res.Done = fmt.Sprintf("已%s %d 项", verb, done)
	if len(res.Skipped) > 0 {
		res.Done += fmt.Sprintf("，跳过 %d 项同名的", len(res.Skipped))
	}
	return res, strings.Join(detail, "；"), nil
}

// archive kinds by name, and how to unpack them into a folder.
func unpackCmd(p, dest string) (tool, cmd string) {
	low := strings.ToLower(p)
	switch {
	case strings.HasSuffix(low, ".zip"):
		return "unzip", "unzip -o -q " + shq(p) + " -d " + shq(dest)
	case strings.HasSuffix(low, ".tar"), strings.HasSuffix(low, ".tar.gz"), strings.HasSuffix(low, ".tgz"),
		strings.HasSuffix(low, ".tar.bz2"), strings.HasSuffix(low, ".tbz2"), strings.HasSuffix(low, ".tar.xz"),
		strings.HasSuffix(low, ".txz"), strings.HasSuffix(low, ".tar.zst"):
		return "tar", "tar -xf " + shq(p) + " -C " + shq(dest)
	case strings.HasSuffix(low, ".gz"):
		return "gzip", "gzip -dc " + shq(p) + " > " + shq(path.Join(dest, strings.TrimSuffix(path.Base(p), path.Ext(p))))
	case strings.HasSuffix(low, ".bz2"):
		return "bzip2", "bzip2 -dc " + shq(p) + " > " + shq(path.Join(dest, strings.TrimSuffix(path.Base(p), path.Ext(p))))
	case strings.HasSuffix(low, ".xz"):
		return "xz", "xz -dc " + shq(p) + " > " + shq(path.Join(dest, strings.TrimSuffix(path.Base(p), path.Ext(p))))
	case strings.HasSuffix(low, ".7z"):
		return "7z", "7z x -y -o" + shq(dest) + " " + shq(p)
	case strings.HasSuffix(low, ".rar"):
		return "unrar", "unrar x -o+ " + shq(p) + " " + shq(dest+"/")
	}
	return "", ""
}

// IsArchive says whether the file manager can unpack a file.
func IsArchive(name string) bool {
	tool, _ := unpackCmd(name, "/")
	return tool != ""
}

var installHint = map[string]string{
	"unzip": "apt install unzip（CentOS 用 yum install unzip）", "7z": "apt install p7zip-full（CentOS 用 yum install p7zip）",
	"unrar": "apt install unrar", "zip": "apt install zip（CentOS 用 yum install zip）",
}

func (fc *fileConn) extract(ctx context.Context, p, dest string) (FileOpResult, string, error) {
	if dest == "" {
		dest = path.Dir(p)
	}
	tool, cmd := unpackCmd(p, dest)
	if tool == "" {
		return FileOpResult{}, "", userErr("%s 不是能解压的格式（支持 zip、tar、tar.gz、tgz、tar.bz2、tar.xz、gz、7z、rar）", path.Base(p))
	}
	if !fc.has(ctx, tool) {
		return FileOpResult{}, "", userErr("服务器上没有 %s 命令，可以先在终端安装：%s", tool, orDash(installHint[tool]))
	}
	if err := fc.run(ctx, "mkdir -p -- "+shq(dest)+" && "+cmd); err != nil {
		return FileOpResult{}, "", err
	}
	return FileOpResult{Done: "已解压到 " + dest}, p + " → " + dest, nil
}

func (fc *fileConn) compress(ctx context.Context, op FileOp, paths []string) (FileOpResult, string, error) {
	dir := path.Dir(paths[0])
	names := make([]string, len(paths))
	for i, p := range paths {
		if path.Dir(p) != dir {
			return FileOpResult{}, "", userErr("要压缩的文件要在同一个文件夹里")
		}
		names[i] = path.Base(p)
	}
	name, err := validName(op.Name)
	if err != nil {
		return FileOpResult{}, "", err
	}
	var cmd string
	switch op.Format {
	case "zip":
		if !strings.HasSuffix(strings.ToLower(name), ".zip") {
			name += ".zip"
		}
		if !fc.has(ctx, "zip") {
			return FileOpResult{}, "", userErr("服务器上没有 zip 命令，可以先在终端安装：%s；或者改用 tar.gz", installHint["zip"])
		}
		cmd = "cd " + shq(dir) + " && zip -r -q -y " + shq(name) + " -- " + shqAll(names)
	case "tar.gz", "":
		if !strings.HasSuffix(strings.ToLower(name), ".tar.gz") && !strings.HasSuffix(strings.ToLower(name), ".tgz") {
			name += ".tar.gz"
		}
		cmd = "tar -czf " + shq(path.Join(dir, name)) + " -C " + shq(dir) + " -- " + shqAll(names)
	default:
		return FileOpResult{}, "", userErr("压缩格式只能是 zip 或 tar.gz")
	}
	out := path.Join(dir, name)
	if _, err := fc.sftp.Lstat(out); err == nil {
		return FileOpResult{}, "", &UserError{Msg: name + " 已经存在", Code: "conflict"}
	}
	if err := fc.run(ctx, cmd); err != nil {
		return FileOpResult{}, "", err
	}
	fc.ownLikeParent(out)
	return FileOpResult{Done: "已压缩为 " + name}, strings.Join(paths, "、") + " → " + out, nil
}
