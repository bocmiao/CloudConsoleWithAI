package app

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// The 存储 page: COS buckets, their files and their settings. Files are
// changed directly (and logged), like the server file manager; settings
// changes are checklists, so they are logged and can be undone.

// COSBucketView is one bucket in the list.
type COSBucketView struct {
	Name    string `json:"name"`
	Region  string `json:"region"`
	Created string `json:"created"`
	ACL     string `json:"acl,omitempty"`   // private, public-read, public-read-write
	Level   string `json:"level,omitempty"` // crit, warn: how exposed it is
	Error   string `json:"error,omitempty"`
}

// COSBucketsView lists the buckets and the account's APPID.
type COSBucketsView struct {
	Buckets []COSBucketView `json:"buckets"`
	AppID   string          `json:"appId"`
}

func aclLevel(acl string) string {
	switch acl {
	case "public-read-write":
		return "crit"
	case "public-read":
		return "warn"
	}
	return ""
}

// COSBuckets lists the buckets, each with its access setting.
func (a *App) COSBuckets(ctx context.Context) (COSBucketsView, error) {
	c, err := needTencent(a)
	if err != nil {
		return COSBucketsView{}, err
	}
	bs, err := c.Buckets(ctx)
	if err != nil {
		return COSBucketsView{}, err
	}
	out := COSBucketsView{Buckets: make([]COSBucketView, len(bs))}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, b := range bs {
		out.Buckets[i] = COSBucketView{Name: b.Name, Region: b.Region, Created: b.Created}
		wg.Add(1)
		go func(v *COSBucketView) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			acl, err := c.BucketACL(ctx, v.Name, v.Region)
			if err != nil {
				v.Error = err.Error()
				return
			}
			v.ACL, v.Level = acl.Canned, aclLevel(acl.Canned)
		}(&out.Buckets[i])
	}
	wg.Wait()
	sort.Slice(out.Buckets, func(i, j int) bool { return out.Buckets[i].Name < out.Buckets[j].Name })
	out.AppID = appIDOf(bs)
	if out.AppID == "" {
		out.AppID = a.cosAppID(ctx, c)
	}
	return out, nil
}

func appIDOf(bs []tencent.Bucket) string {
	for _, b := range bs {
		if i := strings.LastIndex(b.Name, "-"); i > 0 {
			return b.Name[i+1:]
		}
	}
	return ""
}

// cosAppID asks the account for its APPID; "" when it cannot be asked.
func (a *App) cosAppID(ctx context.Context, c *tencent.Client) string {
	var out struct {
		AppID json.Number `json:"AppId"`
	}
	if err := c.Call(ctx, "cam", "2019-01-16", "GetUserAppId", map[string]any{}, &out); err != nil {
		return ""
	}
	return out.AppID.String()
}

// COSFinding is a problem with a bucket and how to fix it.
type COSFinding struct {
	Level string   `json:"level"` // crit, warn, info
	Text  string   `json:"text"`
	Fix   string   `json:"fix,omitempty"` // private, referer, policy_public_off, versioning, files
	Files []string `json:"files,omitempty"`
}

// LifeRuleView is a lifecycle rule; rules made elsewhere with conditions
// this page does not edit are shown, and kept as they are.
type LifeRuleView struct {
	actions.LifeRule
	Editable bool   `json:"editable"`
	Summary  string `json:"summary"`
}

// COSDetail is everything the page shows about a bucket.
type COSDetail struct {
	Name         string             `json:"name"`
	Region       string             `json:"region"`
	Host         string             `json:"host"`
	ACL          tencent.ACL        `json:"acl"`
	Referer      tencent.Referer    `json:"referer"`
	CORS         []tencent.CORSRule `json:"cors"`
	Lifecycle    []LifeRuleView     `json:"lifecycle"`
	Versioning   string             `json:"versioning"`
	Encryption   string             `json:"encryption"`
	Website      tencent.Website    `json:"website"`
	Policy       string             `json:"policy"`
	PolicyPublic string             `json:"policyPublic,omitempty"` // read, write: what the policy lets anyone do
	Findings     []COSFinding       `json:"findings"`
	Errors       map[string]string  `json:"errors,omitempty"` // settings that could not be read
}

var sensitiveRe = regexp.MustCompile(`(?i)(\.(sql|sql\.gz|sql\.zip|bak|backup|dump|db|sqlite3?|pem|key|p12|pfx)$|(^|/)\.env|(^|/)id_rsa|wp-config\.php|(^|/)(backup|backups|dump)/)`)

// Archives are what public buckets are often for (downloads); they count
// only when the name says they are a backup of a site or a database.
var sensitiveArchiveRe = regexp.MustCompile(`(?i)(backup|bak|dump|mysql|database|wwwroot|(^|[/_.-])(db|site|www)([/_.-]|$)).*\.(tar|tar\.gz|tgz|zip|7z|rar)$`)

func sensitiveKey(key string) bool {
	return sensitiveRe.MatchString(key) || sensitiveArchiveRe.MatchString(key)
}

// policyPublic says what a bucket policy lets anyone (not signed in) do.
func policyPublic(policy string) string {
	if strings.TrimSpace(policy) == "" {
		return ""
	}
	var p struct {
		Statement []struct {
			Effect    string          `json:"effect"`
			Principal json.RawMessage `json:"principal"`
			Action    json.RawMessage `json:"action"`
		} `json:"statement"`
	}
	if json.Unmarshal([]byte(policy), &p) != nil {
		return ""
	}
	level := ""
	for _, s := range p.Statement {
		pr := strings.ToLower(string(s.Principal))
		if !strings.EqualFold(s.Effect, "allow") || !(strings.Contains(pr, "anyone") || strings.Contains(pr, `"*"`)) {
			continue
		}
		act := strings.ToLower(string(s.Action))
		if strings.Contains(act, "put") || strings.Contains(act, "delete") || strings.Contains(act, "post") || strings.Contains(act, `cos:*`) || strings.Contains(act, `"*"`) {
			return "write"
		}
		level = "read"
	}
	return level
}

// withoutPublic removes the statements of a policy that let anyone in.
func withoutPublic(policy string) (string, int, error) {
	var p map[string]json.RawMessage
	if err := json.Unmarshal([]byte(policy), &p); err != nil {
		return "", 0, err
	}
	key := ""
	for k := range p {
		if strings.EqualFold(k, "statement") {
			key = k
		}
	}
	var stmts []json.RawMessage
	_ = json.Unmarshal(p[key], &stmts)
	var keep []json.RawMessage
	removed := 0
	for _, s := range stmts {
		if policyPublic(`{"statement":[`+string(s)+`]}`) != "" {
			removed++
			continue
		}
		keep = append(keep, s)
	}
	if len(keep) == 0 {
		return "", removed, nil
	}
	p[key], _ = json.Marshal(keep)
	out, _ := json.Marshal(p)
	return string(out), removed, nil
}

func lifeSummary(r actions.LifeRule) string {
	var parts []string
	if r.IADays > 0 {
		parts = append(parts, fmt.Sprintf("%d 天后转低频", r.IADays))
	}
	if r.ArchiveDays > 0 {
		parts = append(parts, fmt.Sprintf("%d 天后转归档", r.ArchiveDays))
	}
	if r.DeepDays > 0 {
		parts = append(parts, fmt.Sprintf("%d 天后转深度归档", r.DeepDays))
	}
	if r.ExpireDays > 0 {
		parts = append(parts, fmt.Sprintf("%d 天后删除", r.ExpireDays))
	}
	if r.NoncurrentDays > 0 {
		parts = append(parts, fmt.Sprintf("历史版本 %d 天后删除", r.NoncurrentDays))
	}
	if r.AbortDays > 0 {
		parts = append(parts, fmt.Sprintf("清理 %d 天前没传完的碎片", r.AbortDays))
	}
	where := "整个桶"
	if r.Prefix != "" {
		where = r.Prefix
	}
	return where + "：" + strings.Join(parts, "，")
}

// lifeViews turns COS's rules into the page's form.
func lifeViews(raw string, rules []tencent.LifecycleRule) []LifeRuleView {
	var inner struct {
		Rules []struct {
			Inner string `xml:",innerxml"`
		} `xml:"Rule"`
	}
	_ = xml.Unmarshal([]byte(raw), &inner)
	out := []LifeRuleView{}
	for i, r := range rules {
		v := LifeRuleView{LifeRule: actions.LifeRule{ID: r.ID, Prefix: r.Filter.Prefix, Enabled: r.Status == "Enabled"}, Editable: true}
		body := ""
		if i < len(inner.Rules) {
			body = inner.Rules[i].Inner
		}
		for _, odd := range []string{"<Tag", "<And", "<Date", "AccessFrequency", "NoncurrentVersionTransition", "ExpiredObjectDeleteMarker"} {
			if strings.Contains(body, odd) {
				v.Editable = false
			}
		}
		for _, t := range r.Transitions {
			switch {
			case t.Days == 0:
				v.Editable = false
			case t.StorageClass == "STANDARD_IA" && v.IADays == 0:
				v.IADays = t.Days
			case t.StorageClass == "ARCHIVE" && v.ArchiveDays == 0:
				v.ArchiveDays = t.Days
			case t.StorageClass == "DEEP_ARCHIVE" && v.DeepDays == 0:
				v.DeepDays = t.Days
			default:
				v.Editable = false
			}
		}
		if r.Expiration != nil {
			v.ExpireDays = r.Expiration.Days
			if r.Expiration.Days == 0 {
				v.Editable = false
			}
		}
		if r.NoncurrentExpiration != nil {
			v.NoncurrentDays = r.NoncurrentExpiration.Days
		}
		if r.AbortUpload != nil {
			v.AbortDays = r.AbortUpload.Days
		}
		v.Summary = lifeSummary(v.LifeRule)
		if !v.Editable {
			v.Summary = "在别处设置的规则（带标签、日期等条件），这里只能保留或删除"
		}
		out = append(out, v)
	}
	return out
}

// COSBucketDetail reads a bucket's settings and checks them.
func (a *App) COSBucketDetail(ctx context.Context, bucket, region string) (COSDetail, error) {
	c, err := needTencent(a)
	if err != nil {
		return COSDetail{}, err
	}
	if err := checkBucket(bucket, region); err != nil {
		return COSDetail{}, err
	}
	d := COSDetail{Name: bucket, Region: region, Host: tencent.COSHost(bucket, region), Errors: map[string]string{}, CORS: []tencent.CORSRule{}, Lifecycle: []LifeRuleView{}, Findings: []COSFinding{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	read := func(name string, f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(); err != nil {
				mu.Lock()
				d.Errors[name] = err.Error()
				mu.Unlock()
			}
		}()
	}
	var acl tencent.ACL
	var raw string
	var rules []tencent.LifecycleRule
	read("acl", func() (err error) { acl, err = c.BucketACL(ctx, bucket, region); return })
	read("referer", func() (err error) { d.Referer, err = c.BucketReferer(ctx, bucket, region); return })
	read("cors", func() (err error) { d.CORS, err = c.BucketCORS(ctx, bucket, region); return })
	read("lifecycle", func() (err error) { raw, rules, err = c.BucketLifecycle(ctx, bucket, region); return })
	read("versioning", func() (err error) { d.Versioning, err = c.BucketVersioning(ctx, bucket, region); return })
	read("encryption", func() (err error) { d.Encryption, err = c.BucketEncryption(ctx, bucket, region); return })
	read("website", func() (err error) { d.Website, err = c.BucketWebsite(ctx, bucket, region); return })
	read("policy", func() (err error) { d.Policy, err = c.BucketPolicy(ctx, bucket, region); return })
	wg.Wait()
	if msg, bad := d.Errors["acl"]; bad && len(d.Errors) >= 8 {
		return COSDetail{}, userErr("%s", msg)
	}
	d.ACL = acl
	if d.CORS == nil {
		d.CORS = []tencent.CORSRule{}
	}
	d.Lifecycle = lifeViews(raw, rules)
	d.PolicyPublic = policyPublic(d.Policy)
	d.Findings = a.cosFindings(ctx, c, d)
	return d, nil
}

func (a *App) cosFindings(ctx context.Context, c *tencent.Client, d COSDetail) []COSFinding {
	out := []COSFinding{}
	publicRead := d.ACL.Canned == "public-read" || d.ACL.Canned == "public-read-write" || d.PolicyPublic != ""
	switch d.ACL.Canned {
	case "public-read-write":
		out = append(out, COSFinding{Level: "crit", Fix: "private",
			Text: "访问权限是「公有读写」：任何人都能往这个桶里上传文件，也能覆盖和删除文件，还可能被用来存放违规内容。"})
	case "public-read":
		if d.Referer.Status != "Enabled" {
			out = append(out, COSFinding{Level: "warn", Fix: "referer",
				Text: "访问权限是「公有读」而且没开防盗链：任何人都能下载、列出桶里的文件；文件被别的网站引用或被刷流量时，流量费算你的。如果桶只是给网站放图片，开启防盗链；不需要公开就改成私有。"})
		} else {
			out = append(out, COSFinding{Level: "info", Fix: "private",
				Text: "访问权限是「公有读」：任何人都能下载桶里的文件，也能列出所有文件名（防盗链只拦别的网站引用，拦不住直接访问）。"})
		}
	}
	switch d.PolicyPublic {
	case "write":
		out = append(out, COSFinding{Level: "crit", Fix: "policy_public_off", Text: "存储桶策略允许任何人（不用登录）上传或删除文件。"})
	case "read":
		out = append(out, COSFinding{Level: "warn", Fix: "policy_public_off", Text: "存储桶策略允许任何人（不用登录）读取文件。"})
	}
	if publicRead {
		// A public bucket holding backups or keys gives them away.
		l, err := c.ListObjects(ctx, d.Name, d.Region, "", "", "", 1000)
		if err == nil {
			var hits []string
			for _, o := range l.Objects {
				if sensitiveKey(o.Key) && !strings.HasSuffix(o.Key, "/") {
					hits = append(hits, o.Key)
				}
			}
			if len(hits) > 0 {
				show := hits
				if len(show) > 8 {
					show = show[:8]
				}
				// Public through the policy only: making the ACL private
				// changes nothing, the anonymous policy rules must go.
				fix := "private"
				if d.ACL.Canned == "private" || d.ACL.Canned == "" {
					fix = "policy_public_off"
				}
				out = append(out, COSFinding{Level: "crit", Fix: fix, Files: show,
					Text: fmt.Sprintf("公开的桶里有 %d 个看起来是备份、数据库或密钥的文件（例如 %s），任何人都能下载。", len(hits), hits[0])})
			}
		}
	}
	for _, r := range d.CORS {
		wild := false
		for _, o := range r.Origins {
			wild = wild || o == "*"
		}
		writes := false
		for _, m := range r.Methods {
			writes = writes || m == "PUT" || m == "POST" || m == "DELETE"
		}
		if wild && writes {
			out = append(out, COSFinding{Level: "warn", Fix: "cors", Text: "跨域规则允许任何网站用 PUT、POST 或 DELETE 访问这个桶，建议只写你自己的网站。"})
			break
		}
	}
	if d.Versioning == "" {
		out = append(out, COSFinding{Level: "info", Fix: "versioning", Text: "没开版本控制：误删或被覆盖的文件找不回。开启后旧版本会保留（也占存储费用，可以用生命周期规则定期清理）。"})
	}
	return out
}

// COSUsageView is what Cloud Monitor says about a bucket.
type COSUsageView struct {
	Available    bool              `json:"available"`
	Note         string            `json:"note,omitempty"`
	StorageBytes float64           `json:"storageBytes"`
	Traffic24h   float64           `json:"traffic24h"`
	TrafficPrev  float64           `json:"trafficPrev"`
	Spike        bool              `json:"spike"`
	Hourly       []tencent.Point   `json:"hourly"`
	Metrics      map[string]string `json:"metrics,omitempty"` // which metrics were read
}

func unitBytes(u string) float64 {
	switch strings.ToUpper(strings.TrimSpace(u)) {
	case "KB":
		return 1 << 10
	case "MB":
		return 1 << 20
	case "GB":
		return 1 << 30
	case "TB":
		return 1 << 40
	}
	return 1
}

// COSUsage reads a bucket's storage and internet traffic. Metric names are
// found by what Cloud Monitor lists for COS, not assumed.
func (a *App) COSUsage(ctx context.Context, bucket, region string) (COSUsageView, error) {
	c, err := needTencent(a)
	if err != nil {
		return COSUsageView{}, err
	}
	if err := checkBucket(bucket, region); err != nil {
		return COSUsageView{}, err
	}
	out := COSUsageView{Hourly: []tencent.Point{}, Metrics: map[string]string{}}
	ms, err := c.Metrics(ctx, region, "QCE/COS")
	if err != nil {
		out.Note = "读不到用量：" + err.Error()
		return out, nil
	}
	var storage, traffic *tencent.Metric
	for i, m := range ms {
		switch {
		case m.Name == "StdStorage" || (storage == nil && strings.Contains(m.CName, "标准存储") && strings.Contains(m.CName, "存储")):
			storage = &ms[i]
		case m.Name == "InternetTraffic" || (traffic == nil && strings.Contains(m.CName, "外网下行流量")):
			traffic = &ms[i]
		}
	}
	appid := bucket[strings.LastIndex(bucket, "-")+1:]
	short := bucket[:strings.LastIndex(bucket, "-")]
	end := time.Now()
	read := func(m *tencent.Metric, period int, since time.Duration) []tencent.Point {
		for _, name := range []string{bucket, short} {
			pts, err := c.MonitorDataDims(ctx, region, "QCE/COS", m.Name, map[string]string{"appid": appid, "bucket": name}, period,
				tencent.TimeArg(end.Add(-since)), tencent.TimeArg(end))
			if err == nil && len(pts) > 0 {
				return pts
			}
		}
		return nil
	}
	pick := func(m *tencent.Metric, want ...int) int {
		for _, w := range want {
			for _, p := range m.Periods {
				if p == w {
					return w
				}
			}
		}
		if len(m.Periods) > 0 {
			return m.Periods[len(m.Periods)-1]
		}
		return want[0]
	}
	if storage != nil {
		out.Metrics["storage"] = storage.Name
		if pts := read(storage, pick(storage, 86400, 3600, 300), 72*time.Hour); len(pts) > 0 {
			out.StorageBytes = pts[len(pts)-1].V * unitBytes(storage.Unit)
			out.Available = true
		}
	}
	if traffic != nil {
		out.Metrics["traffic"] = traffic.Name
		period := pick(traffic, 3600, 300, 60)
		pts := read(traffic, period, 48*time.Hour)
		if len(pts) > 0 {
			out.Available = true
			scale := unitBytes(traffic.Unit)
			cut := end.Add(-24 * time.Hour).Unix()
			hours := map[int64]float64{}
			for _, p := range pts {
				v := p.V * scale
				if p.T >= cut {
					out.Traffic24h += v
				} else {
					out.TrafficPrev += v
				}
				hours[p.T/3600*3600] += v
			}
			for t, v := range hours {
				out.Hourly = append(out.Hourly, tencent.Point{T: t, V: v})
			}
			sort.Slice(out.Hourly, func(i, j int) bool { return out.Hourly[i].T < out.Hourly[j].T })
			// Worth a look: several times the day before, and at least 1 GB.
			out.Spike = out.Traffic24h >= 1<<30 && out.Traffic24h > 3*out.TrafficPrev
		}
	}
	if !out.Available && out.Note == "" {
		out.Note = "腾讯云监控里还没有这个存储桶的用量数据（新建的桶要等一段时间）"
	}
	return out, nil
}

// ---- Files ----

var cosRegionRe = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]+){1,3}$`)
var cosBucketRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?-[0-9]{6,12}$`)

func checkBucket(bucket, region string) error {
	if len(bucket) > 60 || !cosBucketRe.MatchString(bucket) || !cosRegionRe.MatchString(region) {
		return userErr("存储桶或地域不对")
	}
	return nil
}

// checkCOSKey makes sure a file or folder name is one this page handles:
// no leading slash, no empty, "." or ".." parts, no control characters.
func checkCOSKey(key string, folder bool) error {
	name := strings.TrimSuffix(key, "/")
	if key == "" || name == "" || len(key) > 850 || strings.HasPrefix(key, "/") || strings.ContainsFunc(key, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return userErr("文件名不对")
	}
	if folder != strings.HasSuffix(key, "/") {
		return userErr("文件名不对")
	}
	for _, p := range strings.Split(name, "/") {
		if p == "" || p == "." || p == ".." {
			return userErr("文件名里不能有空的、. 或 .. 的部分")
		}
	}
	return nil
}

// COSEntry is a file or folder in the page's listing.
type COSEntry struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Folder       bool   `json:"folder"`
	Size         int64  `json:"size"`
	Modified     string `json:"modified,omitempty"`
	StorageClass string `json:"storageClass,omitempty"`
}

// COSListing is one page of a folder.
type COSListing struct {
	Prefix  string     `json:"prefix"`
	Entries []COSEntry `json:"entries"`
	Next    string     `json:"next,omitempty"`
}

// COSObjects lists a folder of a bucket, folders first.
func (a *App) COSObjects(ctx context.Context, bucket, region, prefix, marker string) (COSListing, error) {
	c, err := needTencent(a)
	if err != nil {
		return COSListing{}, err
	}
	if err := checkBucket(bucket, region); err != nil {
		return COSListing{}, err
	}
	if prefix != "" {
		if err := checkCOSKey(prefix, true); err != nil {
			return COSListing{}, err
		}
	}
	l, err := c.ListObjects(ctx, bucket, region, prefix, "/", marker, 500)
	if err != nil {
		return COSListing{}, err
	}
	out := COSListing{Prefix: prefix, Entries: []COSEntry{}, Next: l.NextMarker}
	for _, f := range l.Folders {
		out.Entries = append(out.Entries, COSEntry{Key: f, Name: strings.TrimSuffix(strings.TrimPrefix(f, prefix), "/"), Folder: true})
	}
	for _, o := range l.Objects {
		if o.Key == prefix { // the folder's own placeholder
			continue
		}
		out.Entries = append(out.Entries, COSEntry{Key: o.Key, Name: strings.TrimPrefix(o.Key, prefix), Size: o.Size, Modified: o.LastModified, StorageClass: o.StorageClass})
	}
	return out, nil
}

// COSUpload stores a file; an existing file is replaced only if asked.
func (a *App) COSUpload(ctx context.Context, bucket, region, key string, body io.Reader, size int64, contentType string, overwrite bool) (COSEntry, error) {
	c, err := needTencent(a)
	if err != nil {
		return COSEntry{}, err
	}
	if err := checkBucket(bucket, region); err != nil {
		return COSEntry{}, err
	}
	if err := checkCOSKey(key, false); err != nil {
		return COSEntry{}, err
	}
	if size < 0 || size > 5<<30 {
		return COSEntry{}, userErr("单个文件最大 5 GB")
	}
	if !overwrite {
		if _, _, exists, err := c.HeadObject(ctx, bucket, region, key); err != nil {
			return COSEntry{}, err
		} else if exists {
			return COSEntry{}, &UserError{Msg: "已经有同名的文件 " + path.Base(key), Code: "conflict"}
		}
	}
	if contentType == "" || contentType == "application/x-www-form-urlencoded" {
		contentType = "application/octet-stream"
	}
	if err := c.PutObject(ctx, bucket, region, key, body, size, contentType); err != nil {
		return COSEntry{}, err
	}
	_ = a.Store.Audit("user", "cos.upload", bucket+"/"+key, humanSize(size))
	return COSEntry{Key: key, Name: path.Base(key), Size: size, Modified: time.Now().UTC().Format(time.RFC3339)}, nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// COSMkdir makes an empty folder.
func (a *App) COSMkdir(ctx context.Context, bucket, region, key string) error {
	c, err := needTencent(a)
	if err != nil {
		return err
	}
	if err := checkBucket(bucket, region); err != nil {
		return err
	}
	if err := checkCOSKey(key, true); err != nil {
		return err
	}
	if err := c.PutObject(ctx, bucket, region, key, nil, 0, ""); err != nil {
		return err
	}
	_ = a.Store.Audit("user", "cos.mkdir", bucket+"/"+key, "")
	return nil
}

// cosMaxBulk bounds how many files one delete or folder rename touches;
// bigger jobs belong to lifecycle rules.
const cosMaxBulk = 10000

// allUnder lists every key under a folder, at most limit of them.
func allUnder(ctx context.Context, c *tencent.Client, bucket, region, prefix string, limit int) ([]string, error) {
	var keys []string
	marker := ""
	for {
		l, err := c.ListObjects(ctx, bucket, region, prefix, "", marker, 1000)
		if err != nil {
			return nil, err
		}
		for _, o := range l.Objects {
			keys = append(keys, o.Key)
		}
		if len(keys) > limit {
			return nil, userErr("%s 里的文件超过 %d 个，太多了。请用生命周期规则批量删除", prefix, limit)
		}
		if l.NextMarker == "" {
			return keys, nil
		}
		marker = l.NextMarker
	}
}

// COSDelete removes files, and folders with everything in them. It
// cannot be undone unless the bucket keeps versions.
func (a *App) COSDelete(ctx context.Context, bucket, region string, keys []string) (int, error) {
	c, err := needTencent(a)
	if err != nil {
		return 0, err
	}
	if err := checkBucket(bucket, region); err != nil {
		return 0, err
	}
	var all []string
	for _, k := range keys {
		folder := strings.HasSuffix(k, "/")
		if err := checkCOSKey(k, folder); err != nil {
			return 0, err
		}
		if !folder {
			all = append(all, k)
			continue
		}
		under, err := allUnder(ctx, c, bucket, region, k, cosMaxBulk)
		if err != nil {
			return 0, err
		}
		all = append(all, under...)
		all = append(all, k)
	}
	seen := map[string]bool{}
	uniq := all[:0]
	for _, k := range all {
		if !seen[k] {
			seen[k] = true
			uniq = append(uniq, k)
		}
	}
	all = uniq
	if len(all) > cosMaxBulk {
		return 0, userErr("一次最多删除 %d 个文件", cosMaxBulk)
	}
	done := 0
	var failed []string
	for i := 0; i < len(all); i += 1000 {
		batch := all[i:min(i+1000, len(all))]
		bad, err := c.DeleteObjects(ctx, bucket, region, batch)
		if err != nil {
			_ = a.Store.Audit("user", "cos.delete", bucket, fmt.Sprintf("删除了 %d 个，之后失败：%v", done, err))
			return done, err
		}
		done += len(batch) - len(bad)
		for k, why := range bad {
			failed = append(failed, k+"："+why)
		}
	}
	_ = a.Store.Audit("user", "cos.delete", bucket, fmt.Sprintf("%s，共 %d 个文件", strings.Join(keys, "、"), done))
	if len(failed) > 0 {
		sort.Strings(failed)
		return done, userErr("有 %d 个没删掉：%s", len(failed), failed[0])
	}
	return done, nil
}

// COSRename moves a file or folder within the bucket (copies, then
// removes the original).
func (a *App) COSRename(ctx context.Context, bucket, region, from, to string) error {
	c, err := needTencent(a)
	if err != nil {
		return err
	}
	if err := checkBucket(bucket, region); err != nil {
		return err
	}
	folder := strings.HasSuffix(from, "/")
	if err := checkCOSKey(from, folder); err != nil {
		return err
	}
	if err := checkCOSKey(to, folder); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	if folder && strings.HasPrefix(to, from) {
		return userErr("不能把文件夹移到它自己里面")
	}
	if !folder {
		if _, _, exists, err := c.HeadObject(ctx, bucket, region, to); err != nil {
			return err
		} else if exists {
			return &UserError{Msg: "已经有同名的文件 " + path.Base(to), Code: "conflict"}
		}
		if err := c.CopyObject(ctx, bucket, region, from, to); err != nil {
			return err
		}
		if err := c.DeleteObject(ctx, bucket, region, from); err != nil {
			return fmt.Errorf("已经复制成 %s，但删除原来的文件失败：%w", to, err)
		}
		_ = a.Store.Audit("user", "cos.rename", bucket+"/"+from, to)
		return nil
	}
	if l, err := c.ListObjects(ctx, bucket, region, to, "", "", 1); err != nil {
		return err
	} else if len(l.Objects) > 0 {
		return &UserError{Msg: "已经有同名的文件夹 " + path.Base(strings.TrimSuffix(to, "/")), Code: "conflict"}
	}
	keys, err := allUnder(ctx, c, bucket, region, from, 1000)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := c.CopyObject(ctx, bucket, region, k, to+strings.TrimPrefix(k, from)); err != nil {
			return fmt.Errorf("复制 %s 失败（已经复制的文件在新文件夹里，原来的都还在）：%w", k, err)
		}
	}
	bad, err := c.DeleteObjects(ctx, bucket, region, keys)
	if err != nil {
		return fmt.Errorf("已经复制到 %s，但删除原来的文件失败：%w", to, err)
	}
	_ = a.Store.Audit("user", "cos.rename", bucket+"/"+from, fmt.Sprintf("%s，共 %d 个文件", to, len(keys)))
	if len(bad) > 0 {
		var left []string
		for k := range bad {
			left = append(left, k)
		}
		sort.Strings(left)
		return userErr("已经复制到 %s，但原来的文件夹里有 %d 个文件没删掉（比如 %s），两边都有", to, len(left), left[0])
	}
	return nil
}

// COSLink is a link to a file: a presigned one that works until it
// expires, and the plain address, which works when the bucket is public.
func (a *App) COSLink(bucket, region, key string, expires time.Duration, download bool) (map[string]string, error) {
	c, err := needTencent(a)
	if err != nil {
		return nil, err
	}
	if err := checkBucket(bucket, region); err != nil {
		return nil, err
	}
	if err := checkCOSKey(key, false); err != nil {
		return nil, err
	}
	if expires <= 0 || expires > 7*24*time.Hour {
		expires = time.Hour
	}
	link := c.PresignedURL(bucket, region, key, expires, download)
	if !download {
		_ = a.Store.Audit("user", "cos.link", bucket+"/"+key, "有效 "+expires.String())
	}
	return map[string]string{"url": link, "public": tencent.PublicURL(bucket, region, key), "expires": time.Now().Add(expires).Format(time.RFC3339)}, nil
}

// ---- Settings, as checklists ----

// COSRequest is a settings change asked for on the 存储 page.
type COSRequest struct {
	Op     string `json:"op"` // create, delete, acl, referer, cors, lifecycle, versioning, encryption, website, policy, policy_public_off
	Bucket string `json:"bucket"`
	Region string `json:"region"`
	ACL    string `json:"acl"`
	Status string `json:"status"` // referer, versioning, encryption, website: on/off/suspend

	RefererType string   `json:"refererType"` // white, black
	Domains     []string `json:"domains"`
	AllowEmpty  bool     `json:"allowEmpty"`

	CORS      []tencent.CORSRule `json:"cors"`
	Lifecycle []actions.LifeRule `json:"lifecycle"`
	Keep      []string           `json:"keep"`

	Index string `json:"index"`
	Error string `json:"error"`
	HTTPS bool   `json:"https"`

	Policy string `json:"policy"`
	// Run executes the checklist right away: the page's form is the
	// confirmation.
	Run bool `json:"run"`
}

// ProposeCOS turns a settings change into a checklist, and runs it when
// asked.
func (a *App) ProposeCOS(ctx context.Context, req COSRequest) (PlanView, error) {
	c, err := needTencent(a)
	if err != nil {
		return PlanView{}, err
	}
	if err := checkBucket(req.Bucket, req.Region); err != nil {
		return PlanView{}, err
	}
	b := req.Bucket
	where := map[string]any{"bucket": b, "region": req.Region}
	param := func(extra map[string]any) map[string]any {
		m := map[string]any{"bucket": b, "region": req.Region}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	var step core.Step
	var title, reason string
	aclName := map[string]string{"private": "私有读写", "public-read": "公有读私有写", "public-read-write": "公有读写"}
	switch req.Op {
	case "create":
		acl := req.ACL
		if acl != "public-read" {
			acl = "private"
		}
		step = core.Step{Capability: "cos.bucket.create", Summary: fmt.Sprintf("新建存储桶 %s（%s，%s）", b, req.Region, aclName[acl]), Params: param(map[string]any{"acl": acl})}
		title, reason = "新建存储桶 "+b, "新建一个"+aclName[acl]+"的存储桶。可以撤销（桶里还没有文件时）。"
	case "delete":
		step = core.Step{Capability: "cos.bucket.delete", Summary: "删除存储桶 " + b, Params: where}
		title, reason = "删除存储桶 "+b, "只能删除空的存储桶；删除后找不回，名字也可能被别人占用。"
	case "acl":
		if aclName[req.ACL] == "" {
			return PlanView{}, userErr("访问权限不对")
		}
		step = core.Step{Capability: "cos.acl.set", Summary: fmt.Sprintf("把 %s 的访问权限改为「%s」", b, aclName[req.ACL]), Params: param(map[string]any{"acl": req.ACL})}
		title = "修改访问权限：" + b
		reason = map[string]string{
			"private":           "改成私有读写后，只有你（和你授权的账号）能访问；直接用文件地址访问会被拒绝，网站如果直接引用这个桶的文件会显示不出来（可以改用 EdgeOne 回源私有桶或临时链接）。",
			"public-read":       "改成公有读私有写后，任何人都能下载和列出桶里的文件，建议同时开启防盗链。",
			"public-read-write": "改成公有读写后，任何人都能上传、覆盖和删除桶里的文件，非常危险，除非你清楚自己在做什么。",
		}[req.ACL]
	case "referer":
		on := req.Status == "on"
		typ := "white"
		if req.RefererType == "black" {
			typ = "black"
		}
		allow := "yes"
		if !req.AllowEmpty {
			allow = "no"
		}
		step = core.Step{Capability: "cos.referer.set", Params: param(map[string]any{"status": map[bool]string{true: "on", false: "off"}[on], "type": typ,
			"domains": strings.Join(req.Domains, "\n"), "allow_empty": allow})}
		if on {
			step.Summary = fmt.Sprintf("开启 %s 的防盗链（%s：%s；%s空 Referer）", b, map[string]string{"white": "白名单", "black": "黑名单"}[typ],
				strings.Join(req.Domains, "、"), map[string]string{"yes": "允许", "no": "拒绝"}[allow])
			reason = "只有名单里的网站能引用这个桶的文件（黑名单相反）。允许空 Referer 时，直接打开链接、App 里访问不受影响。"
		} else {
			step.Summary, reason = "关闭 "+b+" 的防盗链", "关闭后任何网站都能引用这个桶的文件。"
		}
		title = "防盗链：" + b
	case "cors":
		rules, _ := json.Marshal(req.CORS)
		if req.CORS == nil {
			rules = []byte("[]")
		}
		step = core.Step{Capability: "cos.cors.set", Summary: fmt.Sprintf("把 %s 的跨域规则设为 %d 条", b, len(req.CORS)), Params: param(map[string]any{"rules": string(rules)})}
		title, reason = "跨域规则："+b, "网页里的脚本直接访问这个桶（比如浏览器直传）时需要跨域规则；只用来放图片、下载文件的桶不需要。"
	case "lifecycle":
		rules, _ := json.Marshal(req.Lifecycle)
		if req.Lifecycle == nil {
			rules = []byte("[]")
		}
		var parts []string
		deletes := false
		for _, r := range req.Lifecycle {
			parts = append(parts, lifeSummary(r))
			deletes = deletes || (r.Enabled && (r.ExpireDays > 0 || r.NoncurrentDays > 0))
		}
		sum := fmt.Sprintf("把 %s 的生命周期规则设为 %d 条", b, len(req.Lifecycle)+len(req.Keep))
		if len(parts) > 0 {
			sum += "：" + strings.Join(parts, "；")
		}
		step = core.Step{Capability: "cos.lifecycle.set", Summary: sum, Params: param(map[string]any{"rules": string(rules), "keep": strings.Join(req.Keep, ",")})}
		title, reason = "生命周期规则："+b, "规则每天执行一次，按上传时间计算天数。"
		if deletes {
			reason += "注意：到期删除的文件找不回，撤销这一步只能恢复规则本身。"
		}
	case "versioning":
		if req.Status != "on" && req.Status != "suspend" {
			return PlanView{}, userErr("版本控制只能开启或暂停")
		}
		step = core.Step{Capability: "cos.versioning.set", Summary: map[string]string{"on": "开启 ", "suspend": "暂停 "}[req.Status] + b + " 的版本控制", Params: param(map[string]any{"status": req.Status})}
		title = "版本控制：" + b
		reason = map[string]string{"on": "开启后，覆盖和删除的文件会保留历史版本，误删也能找回；历史版本同样按存储量收费，可以加一条生命周期规则定期删除旧版本。开启过的桶只能暂停，不能关闭。",
			"suspend": "暂停后，之后覆盖和删除的文件不再保留历史版本；已经保留的历史版本还在。"}[req.Status]
	case "encryption":
		on := req.Status == "on"
		step = core.Step{Capability: "cos.encryption.set", Summary: map[bool]string{true: "开启 ", false: "关闭 "}[on] + b + " 的服务端加密（SSE-COS）",
			Params: param(map[string]any{"status": map[bool]string{true: "on", false: "off"}[on]})}
		title, reason = "服务端加密："+b, "只影响之后上传的文件；文件在 COS 的磁盘上加密保存，下载时自动解密，对使用没有影响。"
	case "website":
		on := req.Status == "on"
		https := "no"
		if req.HTTPS {
			https = "yes"
		}
		step = core.Step{Capability: "cos.website.set", Params: param(map[string]any{"status": map[bool]string{true: "on", false: "off"}[on], "index": req.Index, "error": req.Error, "https": https})}
		if on {
			step.Summary = fmt.Sprintf("开启 %s 的静态网站（首页 %s）", b, orDefault(req.Index, "index.html"))
			reason = "开启后可以用静态网站地址访问桶里的网页；存储桶要公有读才能访问。自己的域名建议通过 EdgeOne 接入。"
		} else {
			step.Summary, reason = "关闭 "+b+" 的静态网站", "关闭后静态网站地址不能再访问。"
		}
		title = "静态网站：" + b
	case "policy", "policy_public_off":
		policy := strings.TrimSpace(req.Policy)
		if req.Op == "policy_public_off" {
			cur, err := c.BucketPolicy(ctx, b, req.Region)
			if err != nil {
				return PlanView{}, err
			}
			next, removed, err := withoutPublic(cur)
			if err != nil || removed == 0 {
				return PlanView{}, userErr("存储桶策略里没有允许任何人访问的规则")
			}
			policy = next
			step = core.Step{Capability: "cos.policy.set", Summary: fmt.Sprintf("去掉 %s 的存储桶策略里允许任何人访问的 %d 条规则", b, removed), Params: param(map[string]any{"policy": policy})}
		} else {
			if policy != "" && !json.Valid([]byte(policy)) {
				return PlanView{}, userErr("存储桶策略要是 JSON")
			}
			step = core.Step{Capability: "cos.policy.set", Summary: map[bool]string{true: "修改 ", false: "删除 "}[policy != "" && policy != "{}"] + b + " 的存储桶策略",
				Params: param(map[string]any{"policy": policy})}
		}
		title, reason = "存储桶策略："+b, "存储桶策略可以给其他账号或所有人授权；改错可能让文件被公开或让你的程序访问不了。"
	default:
		return PlanView{}, userErr("不支持的操作 %q", req.Op)
	}
	if _, err := actions.Resolve(step.Capability, step.Params, "-"); err != nil {
		return PlanView{}, userErr("%v", err)
	}
	p, _, err := a.proposePlan(ctx, "user", 0, title, reason, []core.Step{step})
	if err != nil {
		return PlanView{}, err
	}
	if req.Run {
		return a.ExecutePlan(p.ID, []int{0})
	}
	return a.Plan(p.ID)
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ---- For the AI ----

func (a *App) toolTencentCOS(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Bucket string `json:"bucket"`
		Region string `json:"region"`
		Prefix string `json:"prefix"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	if arg.Bucket == "" {
		return a.cloudRead(ctx, "查询对象存储（COS）", func(c *tencent.Client) (string, error) {
			v, err := a.COSBuckets(ctx)
			if err != nil {
				return "", err
			}
			if len(v.Buckets) == 0 {
				return "COS 里没有存储桶。APPID：" + orDash(v.AppID), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "APPID：%s（存储桶名称的后缀）\n", orDash(v.AppID))
			for _, x := range v.Buckets {
				fmt.Fprintf(&b, "%s 地域=%s 访问权限=%s 创建=%s", x.Name, x.Region, orDash(x.ACL), x.Created)
				if x.Error != "" {
					fmt.Fprintf(&b, "（读取权限失败：%s）", x.Error)
				}
				b.WriteString("\n")
			}
			return b.String(), nil
		})
	}
	if arg.Region == "" {
		if v, err := a.COSBuckets(ctx); err == nil {
			for _, x := range v.Buckets {
				if x.Name == arg.Bucket {
					arg.Region = x.Region
				}
			}
		}
	}
	return a.cloudRead(ctx, "查询存储桶 "+arg.Bucket, func(c *tencent.Client) (string, error) {
		d, err := a.COSBucketDetail(ctx, arg.Bucket, arg.Region)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "存储桶 %s（%s）访问权限=%s\n", d.Name, d.Region, d.ACL.Canned)
		fmt.Fprintf(&b, "防盗链=%s %s %s 空Referer=%s\n", d.Referer.Status, d.Referer.Type, strings.Join(d.Referer.Domains, ","), d.Referer.EmptyRefer)
		fmt.Fprintf(&b, "跨域规则 %d 条", len(d.CORS))
		for _, r := range d.CORS {
			fmt.Fprintf(&b, "；来源 %s 方法 %s", strings.Join(r.Origins, ","), strings.Join(r.Methods, ","))
		}
		b.WriteString("\n生命周期规则：")
		if len(d.Lifecycle) == 0 {
			b.WriteString("无")
		}
		for _, r := range d.Lifecycle {
			fmt.Fprintf(&b, "\n- %s（%s）%s", r.ID, map[bool]string{true: "启用", false: "停用"}[r.Enabled], r.Summary)
			if !r.Editable {
				b.WriteString("（不能用 cos.lifecycle.set 编辑，修改时放进 keep 原样保留）")
			}
		}
		fmt.Fprintf(&b, "\n版本控制=%s 服务端加密=%s 静态网站=%v 存储桶策略允许匿名=%s\n", orDash(d.Versioning), orDash(d.Encryption), d.Website.Enabled, orDash(d.PolicyPublic))
		for _, f := range d.Findings {
			fmt.Fprintf(&b, "发现（%s）：%s\n", f.Level, f.Text)
		}
		for k, v := range d.Errors {
			fmt.Fprintf(&b, "读不到 %s：%s\n", k, v)
		}
		l, err := c.ListObjects(ctx, arg.Bucket, arg.Region, arg.Prefix, "/", "", 50)
		if err == nil {
			fmt.Fprintf(&b, "%s 下的内容（最多 50 个）：", orDefault(arg.Prefix, "根目录"))
			for _, f := range l.Folders {
				b.WriteString("\n  " + f)
			}
			for _, o := range l.Objects {
				fmt.Fprintf(&b, "\n  %s %s", o.Key, humanSize(o.Size))
			}
			if l.NextMarker != "" {
				b.WriteString("\n  ……还有更多")
			}
		}
		return b.String(), nil
	})
}
