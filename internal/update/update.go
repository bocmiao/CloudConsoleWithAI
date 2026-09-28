// Package update finds out whether a newer Miao Panel has been released
// and puts it in place of the running program: the release's file for
// this system is downloaded next to the program, checked against the
// release's SHA256SUMS.txt, and renamed over it.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// LatestURL is where GitHub tells the newest release.
const LatestURL = "https://api.github.com/repos/bocmiao/CloudConsoleWithAI/releases/latest"

// Release is a published version.
type Release struct {
	Version   string  `json:"version"` // v0.3.0
	URL       string  `json:"url"`     // its page on GitHub
	Notes     string  `json:"notes"`
	Published string  `json:"published"`
	Assets    []Asset `json:"-"`
}

// Asset is one file of a release.
type Asset struct {
	Name string
	URL  string
	Size int64
}

// Checker talks to GitHub; tests point it elsewhere.
type Checker struct {
	URL  string // LatestURL when empty
	HTTP *http.Client
}

func (c *Checker) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

// Latest reads the newest release.
func (c *Checker) Latest(ctx context.Context) (Release, error) {
	u := c.URL
	if u == "" {
		u = LatestURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "MiaoPanel-Update")
	res, err := c.client().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("连不上 GitHub：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub 返回 %d", res.StatusCode)
	}
	var r struct {
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&r); err != nil {
		return Release{}, fmt.Errorf("读不懂 GitHub 的回答：%w", err)
	}
	if !versionRe(r.TagName) {
		return Release{}, fmt.Errorf("GitHub 上最新的版本号 %q 不对", r.TagName)
	}
	rel := Release{Version: r.TagName, URL: r.HTMLURL, Notes: r.Body, Published: r.PublishedAt}
	for _, a := range r.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size})
	}
	return rel, nil
}

// parse reads v1.2.3 (a -suffix is a pre-release and sorts before).
func parse(v string) (nums [3]int, pre bool, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], true
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nums, pre, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, pre, false
		}
		nums[i] = n
	}
	return nums, pre, true
}

func versionRe(v string) bool { _, _, ok := parse(v); return ok && strings.HasPrefix(v, "v") }

// Newer says whether latest is a newer version than current. A current
// version that is not a release (dev builds) is never older.
func Newer(latest, current string) bool {
	l, lpre, ok1 := parse(latest)
	c, cpre, ok2 := parse(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return cpre && !lpre
}

// IsRelease says whether a version string is a released version.
func IsRelease(v string) bool { return versionRe(v) }

// AssetName is the release's file for a system.
func AssetName(goos, goarch string) string {
	n := "MiaoPanel-" + goos + "-" + goarch
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

func (c *Checker) get(ctx context.Context, url string, w io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "MiaoPanel-Update")
	res, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("下载失败：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：GitHub 返回 %d", res.StatusCode)
	}
	n, err := io.Copy(w, io.LimitReader(res.Body, limit+1))
	if err == nil && n > limit {
		err = errors.New("下载的文件太大了")
	}
	return err
}

// Download fetches the release's file for this system into dir (so it can
// be renamed over the program) and checks its SHA-256 against the
// release's SHA256SUMS.txt. It returns the new file's path.
func (c *Checker) Download(ctx context.Context, rel Release, goos, goarch, dir string) (string, error) {
	want := AssetName(goos, goarch)
	var file, sums Asset
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			file = a
		case "SHA256SUMS.txt":
			sums = a
		}
	}
	if file.URL == "" {
		return "", fmt.Errorf("%s 里没有这个系统的文件（%s）", rel.Version, want)
	}
	if sums.URL == "" {
		return "", fmt.Errorf("%s 里没有校验文件 SHA256SUMS.txt，没有更新", rel.Version)
	}
	var list strings.Builder
	if err := c.get(ctx, sums.URL, &list, 64<<10); err != nil {
		return "", err
	}
	expect := ""
	sc := bufio.NewScanner(strings.NewReader(list.String()))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == want {
			expect = strings.ToLower(f[0])
		}
	}
	if len(expect) != 64 {
		return "", fmt.Errorf("SHA256SUMS.txt 里没有 %s 的校验值，没有更新", want)
	}
	tmp, err := os.CreateTemp(dir, ".miaopanel-update-*")
	if err != nil {
		return "", fmt.Errorf("程序所在的目录不能写入（%v），请手动下载新版本", err)
	}
	h := sha256.New()
	err = c.get(ctx, file.URL, io.MultiWriter(tmp, h), 300<<20)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != expect {
		err = errors.New("下载的文件和发布时的校验值对不上，没有更新")
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// Install puts the downloaded program in place of exe. On Windows the
// running program cannot be replaced, only renamed, so it becomes
// exe.old (removed on the next start).
func Install(exe, next string) error {
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return fmt.Errorf("没能换下正在运行的程序：%w", err)
		}
		if err := os.Rename(next, exe); err != nil {
			_ = os.Rename(old, exe)
			return fmt.Errorf("没能放入新程序：%w", err)
		}
		return nil
	}
	if err := os.Rename(next, exe); err != nil {
		return fmt.Errorf("没能放入新程序：%w", err)
	}
	return nil
}

// Executable is the running program's path, links resolved.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return exe, nil
}

// Writable says whether a new program can be put in dir: a systemd
// service is usually kept from writing where its program is.
func Writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".miaopanel-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// CleanUp removes what an update on Windows left behind.
func CleanUp() {
	if exe, err := Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}

// InDocker says whether Miao Panel runs in a container, where it is
// updated by pulling a new image instead.
func InDocker() bool {
	if os.Getenv("MIAO_DOCKER") == "1" {
		return true
	}
	_, err := os.Stat("/.dockerenv")
	return err == nil
}
