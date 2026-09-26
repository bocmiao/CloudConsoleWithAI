package app

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
)

// EdgeOne keeps every request of the last 31 days as hourly offline log
// packages: gzipped JSON lines with the visitor's real IP. Counting them
// is the most accurate view of a site behind EdgeOne. Downloaded packages
// are kept in the cache folder, so each is fetched once.

// eoLogBudget caps how much log (uncompressed) one count reads; beyond it
// the oldest packages are left out.
var eoLogBudget int64 = 3 << 30

// eoLine is one request in an offline log.
type eoLine struct {
	RequestTime string  `json:"RequestTime"`
	ClientIP    string  `json:"ClientIP"`
	Host        string  `json:"RequestHost"`
	Method      string  `json:"RequestMethod"`
	URL         string  `json:"RequestUrl"`
	Query       string  `json:"RequestUrlQueryString"`
	Referer     string  `json:"RequestReferer"`
	UA          string  `json:"RequestUA"`
	Status      flexInt `json:"EdgeResponseStatusCode"`
	Bytes       flexInt `json:"EdgeResponseBytes"`
}

// flexInt reads a number written as a number or as a string.
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "-" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	*n = flexInt(v)
	return err
}

func (a *App) cacheDir() string {
	if a.CacheDir != "" {
		return a.CacheDir
	}
	return filepath.Join(os.TempDir(), "miaopanel-cache")
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type eoPackage struct {
	zone string
	log  tencent.L7Log
	path string
}

// gatherEdgeOneVisits counts the last 30 days of every EdgeOne site's
// offline logs.
func (a *App) gatherEdgeOneVisits(ctx context.Context) (VisitsView, error) {
	c := a.tencentClient()
	if c == nil {
		return VisitsView{}, userErr("还没有填写腾讯云密钥")
	}
	zones, err := c.Zones(ctx)
	if err != nil {
		return VisitsView{}, err
	}
	now := time.Now()
	y, m, d := now.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, time.Local).AddDate(0, 0, -29)
	dir := filepath.Join(a.cacheDir(), "eologs")
	var pkgs []eoPackage
	var notes []string
	for _, z := range zones {
		list, err := c.L7Logs(ctx, z.ZoneID, start.Add(-time.Hour), now)
		if err != nil {
			notes = append(notes, "站点 "+z.ZoneName+" 的离线日志读不到："+err.Error())
			continue
		}
		for _, l := range list {
			name := unsafeName.ReplaceAllString(l.Domain+"_"+l.Area+"_"+l.Name, "_")
			pkgs = append(pkgs, eoPackage{zone: z.ZoneID, log: l, path: filepath.Join(dir, unsafeName.ReplaceAllString(z.ZoneID, "_"), name)})
		}
	}
	// Newest first, within the budget.
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].log.StartTime > pkgs[j].log.StartTime })
	var total int64
	for i, p := range pkgs {
		total += p.log.Size
		if total > eoLogBudget && i > 0 {
			notes = append(notes, fmt.Sprintf("日志太多，只统计了最近的 %d 个日志包（从 %s 起）", i, pkgs[i-1].log.StartTime))
			pkgs = pkgs[:i]
			break
		}
	}
	if failed := a.fetchPackages(ctx, pkgs); failed > 0 {
		notes = append(notes, fmt.Sprintf("有 %d 个日志包下载失败，这部分没有统计", failed))
	}
	pruneCache(dir, pkgs)

	counter := visits.NewCounter("edgeone", now)
	for _, p := range pkgs {
		if err := countPackage(counter, p); err != nil && !os.IsNotExist(err) {
			notes = append(notes, "读取日志包 "+p.log.Name+" 出错："+err.Error())
		}
	}
	rep := counter.Report()
	rep.Notes = append(rep.Notes, notes...)
	if len(pkgs) == 0 {
		rep.Problem = "EdgeOne 还没有离线日志：新接入的站点要等访问产生后一小时左右才有；也可能这个账号没有 EdgeOne 站点，或者密钥没有 EdgeOne 的读取权限"
	}
	a.enrichVisits(ctx, &rep, 0)
	return VisitsView{Report: rep, Source: "edgeone", Title: "EdgeOne 日志", CheckedAt: now.UTC().Format(time.RFC3339)}, nil
}

// fetchPackages downloads the packages not in the cache yet and returns
// how many failed.
func (a *App) fetchPackages(ctx context.Context, pkgs []eoPackage) int {
	client := &http.Client{Timeout: 3 * time.Minute}
	var failed int
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, p := range pkgs {
		if _, err := os.Stat(p.path); err == nil {
			continue
		}
		wg.Add(1)
		go func(p eoPackage) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := download(ctx, client, p.log.URL, p.path); err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()
	return failed
}

func download(ctx context.Context, client *http.Client, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// pruneCache removes packages that are no longer listed (older than a
// month) and leftovers of interrupted downloads.
func pruneCache(dir string, keep []eoPackage) {
	want := map[string]bool{}
	for _, p := range keep {
		want[p.path] = true
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !want[path] {
			if info, err := d.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
				_ = os.Remove(path)
			}
		}
		return nil
	})
}

// countPackage counts one package's requests.
func countPackage(counter *visits.Counter, p eoPackage) error {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return err
	}
	var r io.Reader = bytes.NewReader(data)
	if gz, err := gzip.NewReader(bytes.NewReader(data)); err == nil {
		r = gz
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e eoLine
		if json.Unmarshal(line, &e) != nil {
			counter.Unparsed(p.log.Domain)
			continue
		}
		t, err := time.Parse(time.RFC3339, e.RequestTime)
		site := strings.ToLower(e.Host)
		if site == "" {
			site = p.log.Domain
		}
		if err != nil || e.ClientIP == "" {
			counter.Unparsed(site)
			continue
		}
		target := e.URL
		if e.Query != "" && e.Query != "-" {
			target += "?" + strings.TrimPrefix(e.Query, "?")
		}
		counter.Add(site, t.In(time.Local), e.ClientIP, e.Method, target, int(e.Status), int64(e.Bytes), e.Referer, e.UA, true)
	}
	return sc.Err()
}
