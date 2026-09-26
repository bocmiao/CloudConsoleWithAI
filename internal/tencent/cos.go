package tencent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// COS (对象存储) has its own XML API, signed with COS's "q-sign" scheme
// (HMAC-SHA1). The signing and the XML follow the official Go SDK
// (github.com/tencentyun/cos-go-sdk-v5).

// Bucket is a COS bucket; Name ends with -<APPID>.
type Bucket struct {
	Name    string `xml:"Name" json:"name"`
	Region  string `xml:"Location" json:"region"`
	Created string `xml:"CreationDate" json:"created"`
	Type    string `xml:"BucketType" json:"type,omitempty"`
}

// COSError is an error returned by COS.
type COSError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
}

func (e *COSError) Error() string {
	switch e.Code {
	case "AccessDenied":
		return "这个腾讯云子账号没有对象存储（COS）的权限，请在访问管理里给它授权（QcloudCOSFullAccess，只看的话 QcloudCOSReadOnlyAccess）"
	case "SignatureDoesNotMatch", "InvalidAccessKeyId":
		return "腾讯云 SecretId 或 SecretKey 不对，请重新复制"
	case "RequestTimeTooSkewed":
		return "电脑的时间不准，对象存储拒绝了请求，请把电脑时间校准后再试"
	case "NoSuchBucket":
		return "存储桶不存在（可能已经被删除）"
	case "BucketNotEmpty":
		return "存储桶里还有文件（或者历史版本、没传完的碎片），要先清空才能删除"
	case "BucketAlreadyExists", "BucketAlreadyOwnedByYou":
		return "这个存储桶名字已经被用了，换一个名字"
	case "InvalidBucketName":
		return "存储桶名字不对：只能用小写字母、数字和中划线"
	case "TooManyBuckets":
		return "存储桶数量已经到上限了"
	case "NoSuchKey":
		return "文件不存在（可能已经被删除）"
	}
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("对象存储返回错误：%s（%s）", msg, e.Code)
}

// IsCOSCode reports whether err is a COS error with that code.
func IsCOSCode(err error, code string) bool {
	var e *COSError
	return errors.As(err, &e) && e.Code == code
}

// notSet reports whether err means a bucket setting was never made.
func notSet(err error) bool {
	var e *COSError
	return errors.As(err, &e) && e.Status == http.StatusNotFound && strings.HasPrefix(e.Code, "NoSuch") && e.Code != "NoSuchBucket"
}

// COSHost is the host of a bucket, or of the service with no bucket.
func COSHost(bucket, region string) string {
	if bucket == "" {
		return "service.cos.myqcloud.com"
	}
	return bucket + ".cos." + region + ".myqcloud.com"
}

// cosEncode is the SDK's encodeURIComponent: everything but letters,
// digits and -_.!~*'() is percent-encoded, and whatever keep lists.
func cosEncode(s string, keep string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', strings.IndexByte("-_.!~*'()", c) >= 0, strings.IndexByte(keep, c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// safeEncode is the SDK's safeURLEncode, used in what is signed.
func safeEncode(s string) string {
	s = cosEncode(s, "")
	for _, r := range [][2]string{{"!", "%21"}, {"'", "%27"}, {"(", "%28"}, {")", "%29"}, {"*", "%2A"}} {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	return s
}

func signHeader(k string) bool {
	switch k {
	case "host", "range", "cache-control", "content-disposition", "content-encoding", "content-type", "content-md5",
		"expires", "if-match", "if-none-match", "if-modified-since", "if-unmodified-since", "origin":
		return true
	}
	return strings.HasPrefix(k, "x-cos-")
}

// signPairs formats parameters or headers the way COS signs them:
// lower-cased encoded keys, encoded values, sorted, joined with &.
func signPairs(vals map[string][]string, keep func(string) bool) (string, []string) {
	pairs := map[string][]string{}
	var keys []string
	for k, vs := range vals {
		lk := strings.ToLower(safeEncode(k))
		if keep != nil && !keep(strings.ToLower(k)) {
			continue
		}
		for _, v := range vs {
			pairs[lk] = append(pairs[lk], safeEncode(v))
			keys = append(keys, lk)
		}
	}
	sort.Strings(keys)
	var names []string
	for k := range pairs {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []string
	for _, k := range names {
		vs := pairs[k]
		sort.Strings(vs)
		for _, v := range vs {
			out = append(out, k+"="+v)
		}
	}
	return strings.Join(out, "&"), keys
}

func hmacSHA1Hex(key, msg string) string {
	h := hmac.New(sha1.New, []byte(key))
	h.Write([]byte(msg))
	return hex.EncodeToString(h.Sum(nil))
}

// COSAuthorization signs a request: path is the decoded path ("/" plus the
// object key), header must hold Host.
func COSAuthorization(secretID, secretKey, method, path string, query url.Values, header http.Header, start, end time.Time) string {
	keyTime := fmt.Sprintf("%d;%d", start.Unix(), end.Unix())
	signKey := hmacSHA1Hex(secretKey, keyTime)
	params, paramList := signPairs(query, nil)
	headers, headerList := signPairs(header, signHeader)
	format := strings.ToLower(method) + "\n" + path + "\n" + params + "\n" + headers + "\n"
	sum := sha1.Sum([]byte(format))
	toSign := "sha1\n" + keyTime + "\n" + hex.EncodeToString(sum[:]) + "\n"
	return strings.Join([]string{
		"q-sign-algorithm=sha1", "q-ak=" + secretID, "q-sign-time=" + keyTime, "q-key-time=" + keyTime,
		"q-header-list=" + strings.Join(headerList, ";"), "q-url-param-list=" + strings.Join(paramList, ";"),
		"q-signature=" + hmacSHA1Hex(signKey, toSign),
	}, "&")
}

// cosQuery encodes parameters for the URL; a subresource such as "acl"
// has no value.
func cosQuery(q url.Values) string {
	var keys []string
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			if v == "" {
				parts = append(parts, cosEncode(k, ""))
			} else {
				parts = append(parts, cosEncode(k, "")+"="+cosEncode(v, ""))
			}
		}
	}
	return strings.Join(parts, "&")
}

type cosRequest struct {
	method, bucket, region, key string
	query                       url.Values
	header                      http.Header
	body                        io.Reader
	size                        int64 // -1 when unknown
}

// cosURL is where a request goes, the host it names and the path it signs.
func (c *Client) cosURL(bucket, region, key string) (u *url.URL, host, path string) {
	host = COSHost(bucket, region)
	path = "/" + key
	base := "https://" + host
	if c.Endpoint != nil {
		base = c.Endpoint("cos") // tests: the fake, told the host by the Host header
	}
	u, _ = url.Parse(base)
	prefix := strings.TrimSuffix(u.Path, "/")
	u.Path = prefix + path
	u.RawPath = prefix + "/" + cosEncode(key, "/")
	return u, host, path
}

// cosHTTP carries object transfers, which may take long; the context
// bounds them.
var cosHTTP = &http.Client{}

func (c *Client) cosDo(ctx context.Context, r cosRequest) (*http.Response, error) {
	u, host, path := c.cosURL(r.bucket, r.region, r.key)
	if r.query == nil {
		r.query = url.Values{}
	}
	u.RawQuery = cosQuery(r.query)
	req, err := http.NewRequestWithContext(ctx, r.method, u.String(), r.body)
	if err != nil {
		return nil, err
	}
	req.Host = host
	for k, vs := range r.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if r.body != nil && r.size >= 0 {
		req.ContentLength = r.size
		if r.size == 0 {
			req.Body = http.NoBody
		}
	}
	if r.body == nil && (r.method == http.MethodPut || r.method == http.MethodPost) {
		req.ContentLength = 0
	}
	sign := req.Header.Clone()
	sign.Set("Host", host)
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	t := now()
	req.Header.Set("Authorization", COSAuthorization(c.SecretID, c.SecretKey, r.method, path, r.query, sign, t.Add(-time.Minute), t.Add(time.Hour)))
	if c.Trace != nil {
		what := r.method + " " + host + path
		if q := cosQuery(r.query); q != "" {
			what += "?" + q
		}
		c.Trace("cos", what, nil)
	}
	hc := c.hc
	if r.key != "" && (r.method == http.MethodPut || r.method == http.MethodGet) {
		hc = cosHTTP
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接对象存储失败：%w", err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var e struct {
			Code      string `xml:"Code"`
			Message   string `xml:"Message"`
			RequestID string `xml:"RequestId"`
		}
		_ = xml.Unmarshal(data, &e)
		if e.Code == "" {
			e.Code = strconv.Itoa(resp.StatusCode)
			if resp.StatusCode == http.StatusNotFound {
				e.Code = "NoSuchKey"
			}
		}
		return nil, &COSError{Status: resp.StatusCode, Code: e.Code, Message: e.Message, RequestID: e.RequestID}
	}
	return resp, nil
}

// cosCall runs a request with an optional XML (or other) body and decodes
// an XML reply into out.
func (c *Client) cosCall(ctx context.Context, method, bucket, region, sub string, body []byte, contentType string, out any) error {
	q := url.Values{}
	if sub != "" {
		q.Set(sub, "")
	}
	h := http.Header{}
	var rd io.Reader
	size := int64(-1)
	if body != nil {
		sum := md5.Sum(body)
		h.Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
		if contentType == "" {
			contentType = "application/xml"
		}
		h.Set("Content-Type", contentType)
		rd, size = bytes.NewReader(body), int64(len(body))
	}
	resp, err := c.cosDo(ctx, cosRequest{method: method, bucket: bucket, region: region, query: q, header: h, body: rd, size: size})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if s, ok := out.(*string); ok {
		*s = string(data)
		return nil
	}
	return xml.Unmarshal(data, out)
}

func xmlBody(v any) []byte {
	b, _ := xml.Marshal(v)
	return b
}

// ---- Buckets ----

// Buckets lists all the account's buckets, in every region.
func (c *Client) Buckets(ctx context.Context) ([]Bucket, error) {
	var all []Bucket
	marker := ""
	for i := 0; i < 50; i++ {
		q := url.Values{"max-keys": {"1000"}}
		if marker != "" {
			q.Set("marker", marker)
		}
		resp, err := c.cosDo(ctx, cosRequest{method: http.MethodGet, query: q})
		if err != nil {
			return nil, err
		}
		var out struct {
			Buckets     []Bucket `xml:"Buckets>Bucket"`
			IsTruncated bool     `xml:"IsTruncated"`
			NextMarker  string   `xml:"NextMarker"`
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		all = append(all, out.Buckets...)
		if !out.IsTruncated || out.NextMarker == "" {
			break
		}
		marker = out.NextMarker
	}
	return all, nil
}

// CreateBucket makes a bucket with a canned ACL (private, public-read).
func (c *Client) CreateBucket(ctx context.Context, bucket, region, acl string) error {
	h := http.Header{}
	if acl != "" {
		h.Set("x-cos-acl", acl)
	}
	resp, err := c.cosDo(ctx, cosRequest{method: http.MethodPut, bucket: bucket, region: region, header: h})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// DeleteBucket removes an empty bucket.
func (c *Client) DeleteBucket(ctx context.Context, bucket, region string) error {
	return c.cosCall(ctx, http.MethodDelete, bucket, region, "", nil, "", nil)
}

// ACL is a bucket's (or object's) access control list: the canned
// setting and any grants to other accounts, in x-cos-grant-* form.
type ACL struct {
	Canned string            `json:"canned"` // private, public-read, public-read-write
	Grants map[string]string `json:"grants,omitempty"`
}

// AllUsers identifies anonymous visitors in COS grants.
const (
	cosAllUsersURI = "http://cam.qcloud.com/groups/global/AllUsers"
	cosAnyone      = "qcs::cam::anyone:anyone"
)

// BucketACL reads a bucket's ACL.
func (c *Client) BucketACL(ctx context.Context, bucket, region string) (ACL, error) {
	var out struct {
		Owner struct {
			ID string `xml:"ID"`
		} `xml:"Owner"`
		Grants []struct {
			Grantee struct {
				ID  string `xml:"ID"`
				URI string `xml:"URI"`
			} `xml:"Grantee"`
			Permission string `xml:"Permission"`
		} `xml:"AccessControlList>Grant"`
	}
	if err := c.cosCall(ctx, http.MethodGet, bucket, region, "acl", nil, "", &out); err != nil {
		return ACL{}, err
	}
	public := map[string]bool{}
	grants := map[string][]string{}
	for _, g := range out.Grants {
		switch {
		case g.Grantee.ID == cosAnyone || g.Grantee.URI == cosAllUsersURI:
			public[g.Permission] = true
		case g.Grantee.ID != "" && g.Grantee.ID != out.Owner.ID:
			grants[g.Permission] = append(grants[g.Permission], `id="`+g.Grantee.ID+`"`)
		}
	}
	a := ACL{Canned: "private", Grants: map[string]string{}}
	switch {
	case public["FULL_CONTROL"] || (public["READ"] && public["WRITE"]):
		a.Canned = "public-read-write"
	case public["READ"]:
		a.Canned = "public-read"
	}
	names := map[string]string{"READ": "x-cos-grant-read", "WRITE": "x-cos-grant-write", "FULL_CONTROL": "x-cos-grant-full-control",
		"READ_ACP": "x-cos-grant-read-acp", "WRITE_ACP": "x-cos-grant-write-acp"}
	for perm, ids := range grants {
		if h := names[perm]; h != "" {
			sort.Strings(ids)
			a.Grants[h] = strings.Join(ids, ",")
		}
	}
	return a, nil
}

// SetBucketACL replaces a bucket's ACL.
func (c *Client) SetBucketACL(ctx context.Context, bucket, region string, a ACL) error {
	h := http.Header{}
	h.Set("x-cos-acl", a.Canned)
	for k, v := range a.Grants {
		h.Set(k, v)
	}
	resp, err := c.cosDo(ctx, cosRequest{method: http.MethodPut, bucket: bucket, region: region, query: url.Values{"acl": {""}}, header: h})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Referer is a bucket's hotlink protection.
type Referer struct {
	XMLName    xml.Name `xml:"RefererConfiguration" json:"-"`
	Status     string   `xml:"Status" json:"status"`    // Enabled, Disabled
	Type       string   `xml:"RefererType" json:"type"` // White-List, Black-List
	Domains    []string `xml:"DomainList>Domain" json:"domains"`
	EmptyRefer string   `xml:"EmptyReferConfiguration,omitempty" json:"emptyRefer"` // Allow, Deny
}

// BucketReferer reads the hotlink protection; never set is Disabled.
func (c *Client) BucketReferer(ctx context.Context, bucket, region string) (Referer, error) {
	var r Referer
	err := c.cosCall(ctx, http.MethodGet, bucket, region, "referer", nil, "", &r)
	if notSet(err) || (err == nil && r.Status == "") {
		return Referer{Status: "Disabled", Type: "White-List", Domains: []string{}, EmptyRefer: "Allow"}, nil
	}
	if r.Domains == nil {
		r.Domains = []string{}
	}
	return r, err
}

// SetBucketReferer writes the hotlink protection.
func (c *Client) SetBucketReferer(ctx context.Context, bucket, region string, r Referer) error {
	if r.Type == "" {
		r.Type = "White-List"
	}
	return c.cosCall(ctx, http.MethodPut, bucket, region, "referer", xmlBody(r), "", nil)
}

// CORSRule is one cross-origin rule.
type CORSRule struct {
	ID      string   `xml:"ID,omitempty" json:"id,omitempty"`
	Methods []string `xml:"AllowedMethod" json:"methods"`
	Origins []string `xml:"AllowedOrigin" json:"origins"`
	Headers []string `xml:"AllowedHeader,omitempty" json:"headers,omitempty"`
	MaxAge  int      `xml:"MaxAgeSeconds,omitempty" json:"maxAge,omitempty"`
	Expose  []string `xml:"ExposeHeader,omitempty" json:"expose,omitempty"`
}

// BucketCORS reads the cross-origin rules; none is an empty list.
func (c *Client) BucketCORS(ctx context.Context, bucket, region string) ([]CORSRule, error) {
	var out struct {
		Rules []CORSRule `xml:"CORSRule"`
	}
	err := c.cosCall(ctx, http.MethodGet, bucket, region, "cors", nil, "", &out)
	if notSet(err) {
		return []CORSRule{}, nil
	}
	if out.Rules == nil {
		out.Rules = []CORSRule{}
	}
	return out.Rules, err
}

// SetBucketCORS replaces the cross-origin rules; none removes them.
func (c *Client) SetBucketCORS(ctx context.Context, bucket, region string, rules []CORSRule) error {
	if len(rules) == 0 {
		err := c.cosCall(ctx, http.MethodDelete, bucket, region, "cors", nil, "", nil)
		if notSet(err) {
			return nil
		}
		return err
	}
	body := xmlBody(struct {
		XMLName xml.Name   `xml:"CORSConfiguration"`
		Rules   []CORSRule `xml:"CORSRule"`
	}{Rules: rules})
	return c.cosCall(ctx, http.MethodPut, bucket, region, "cors", body, "", nil)
}

// LifecycleRule is one lifecycle rule, in COS's shape (kept whole, so
// rules made elsewhere survive a change made here).
type LifecycleRule struct {
	ID     string `xml:"ID,omitempty" json:"id"`
	Status string `xml:"Status" json:"status"` // Enabled, Disabled
	Filter struct {
		Prefix string `xml:"Prefix,omitempty" json:"prefix"`
		Inner  string `xml:",innerxml" json:"-"`
	} `xml:"Filter" json:"filter"`
	Transitions []struct {
		Days         int    `xml:"Days,omitempty" json:"days,omitempty"`
		Date         string `xml:"Date,omitempty" json:"date,omitempty"`
		StorageClass string `xml:"StorageClass" json:"storageClass"`
	} `xml:"Transition" json:"transitions,omitempty"`
	Expiration *struct {
		Days int    `xml:"Days,omitempty" json:"days,omitempty"`
		Date string `xml:"Date,omitempty" json:"date,omitempty"`
	} `xml:"Expiration" json:"expiration,omitempty"`
	NoncurrentExpiration *struct {
		Days int `xml:"NoncurrentDays" json:"days"`
	} `xml:"NoncurrentVersionExpiration" json:"noncurrentExpiration,omitempty"`
	AbortUpload *struct {
		Days int `xml:"DaysAfterInitiation" json:"days"`
	} `xml:"AbortIncompleteMultipartUpload" json:"abortUpload,omitempty"`
}

// BucketLifecycle reads the lifecycle rules as XML (kept as is for
// undoing) and as rules.
func (c *Client) BucketLifecycle(ctx context.Context, bucket, region string) (string, []LifecycleRule, error) {
	var raw string
	err := c.cosCall(ctx, http.MethodGet, bucket, region, "lifecycle", nil, "", &raw)
	if notSet(err) {
		return "", []LifecycleRule{}, nil
	}
	if err != nil {
		return "", nil, err
	}
	rules, err := ParseLifecycle(raw)
	return raw, rules, err
}

// ParseLifecycle reads lifecycle rules from COS's XML.
func ParseLifecycle(raw string) ([]LifecycleRule, error) {
	var out struct {
		Rules []LifecycleRule `xml:"Rule"`
	}
	if strings.TrimSpace(raw) == "" {
		return []LifecycleRule{}, nil
	}
	if err := xml.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("读不懂生命周期规则：%w", err)
	}
	if out.Rules == nil {
		out.Rules = []LifecycleRule{}
	}
	return out.Rules, nil
}

// SetBucketLifecycleXML writes lifecycle rules given as COS's XML; empty
// removes them.
func (c *Client) SetBucketLifecycleXML(ctx context.Context, bucket, region, raw string) error {
	if strings.TrimSpace(raw) == "" {
		err := c.cosCall(ctx, http.MethodDelete, bucket, region, "lifecycle", nil, "", nil)
		if notSet(err) {
			return nil
		}
		return err
	}
	return c.cosCall(ctx, http.MethodPut, bucket, region, "lifecycle", []byte(raw), "", nil)
}

// BucketVersioning reads versioning: "" (never turned on), Enabled or
// Suspended.
func (c *Client) BucketVersioning(ctx context.Context, bucket, region string) (string, error) {
	var out struct {
		Status string `xml:"Status"`
	}
	err := c.cosCall(ctx, http.MethodGet, bucket, region, "versioning", nil, "", &out)
	if notSet(err) {
		return "", nil
	}
	return out.Status, err
}

// SetBucketVersioning turns versioning on (Enabled) or pauses it (Suspended).
func (c *Client) SetBucketVersioning(ctx context.Context, bucket, region, status string) error {
	body := xmlBody(struct {
		XMLName xml.Name `xml:"VersioningConfiguration"`
		Status  string   `xml:"Status"`
	}{Status: status})
	return c.cosCall(ctx, http.MethodPut, bucket, region, "versioning", body, "", nil)
}

// BucketEncryption reads server-side encryption: "" (off) or the algorithm.
func (c *Client) BucketEncryption(ctx context.Context, bucket, region string) (string, error) {
	var out struct {
		Algorithm string `xml:"Rule>ApplyServerSideEncryptionByDefault>SSEAlgorithm"`
	}
	err := c.cosCall(ctx, http.MethodGet, bucket, region, "encryption", nil, "", &out)
	if notSet(err) {
		return "", nil
	}
	return out.Algorithm, err
}

// SetBucketEncryption turns encryption with COS-managed keys (AES256) on,
// or off with "".
func (c *Client) SetBucketEncryption(ctx context.Context, bucket, region, algorithm string) error {
	if algorithm == "" {
		err := c.cosCall(ctx, http.MethodDelete, bucket, region, "encryption", nil, "", nil)
		if notSet(err) {
			return nil
		}
		return err
	}
	body := xmlBody(struct {
		XMLName   xml.Name `xml:"ServerSideEncryptionConfiguration"`
		Algorithm string   `xml:"Rule>ApplyServerSideEncryptionByDefault>SSEAlgorithm"`
	}{Algorithm: algorithm})
	return c.cosCall(ctx, http.MethodPut, bucket, region, "encryption", body, "", nil)
}

// Website is a bucket's static website setting.
type Website struct {
	Index    string `json:"index"`
	Error    string `json:"error,omitempty"`
	HTTPS    bool   `json:"https,omitempty"` // redirect every request to HTTPS
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint,omitempty"`
}

// BucketWebsite reads the static website setting.
func (c *Client) BucketWebsite(ctx context.Context, bucket, region string) (Website, error) {
	var out struct {
		Index    string `xml:"IndexDocument>Suffix"`
		Error    string `xml:"ErrorDocument>Key"`
		Protocol string `xml:"RedirectAllRequestsTo>Protocol"`
	}
	err := c.cosCall(ctx, http.MethodGet, bucket, region, "website", nil, "", &out)
	if notSet(err) {
		return Website{}, nil
	}
	if err != nil {
		return Website{}, err
	}
	return Website{Enabled: out.Index != "", Index: out.Index, Error: out.Error, HTTPS: strings.EqualFold(out.Protocol, "https"),
		Endpoint: bucket + ".cos-website." + region + ".myqcloud.com"}, nil
}

// SetBucketWebsite writes the static website setting; not Enabled removes it.
func (c *Client) SetBucketWebsite(ctx context.Context, bucket, region string, w Website) error {
	if !w.Enabled {
		err := c.cosCall(ctx, http.MethodDelete, bucket, region, "website", nil, "", nil)
		if notSet(err) {
			return nil
		}
		return err
	}
	type redirect struct {
		Protocol string `xml:"Protocol"`
	}
	v := struct {
		XMLName  xml.Name  `xml:"WebsiteConfiguration"`
		Index    string    `xml:"IndexDocument>Suffix"`
		Redirect *redirect `xml:"RedirectAllRequestsTo,omitempty"`
		Error    string    `xml:"ErrorDocument>Key,omitempty"`
	}{Index: w.Index, Error: w.Error}
	if w.HTTPS {
		v.Redirect = &redirect{Protocol: "https"}
	}
	return c.cosCall(ctx, http.MethodPut, bucket, region, "website", xmlBody(v), "", nil)
}

// BucketPolicy reads the bucket policy as JSON, "" when there is none.
func (c *Client) BucketPolicy(ctx context.Context, bucket, region string) (string, error) {
	var s string
	err := c.cosCall(ctx, http.MethodGet, bucket, region, "policy", nil, "", &s)
	if notSet(err) {
		return "", nil
	}
	return s, err
}

// SetBucketPolicy writes the bucket policy JSON; "" removes it.
func (c *Client) SetBucketPolicy(ctx context.Context, bucket, region, policy string) error {
	if strings.TrimSpace(policy) == "" {
		err := c.cosCall(ctx, http.MethodDelete, bucket, region, "policy", nil, "", nil)
		if notSet(err) {
			return nil
		}
		return err
	}
	return c.cosCall(ctx, http.MethodPut, bucket, region, "policy", []byte(policy), "application/json", nil)
}

// ---- Objects ----

// Object is a file in a bucket.
type Object struct {
	Key          string `xml:"Key" json:"key"`
	Size         int64  `xml:"Size" json:"size"`
	LastModified string `xml:"LastModified" json:"modified"`
	ETag         string `xml:"ETag" json:"etag,omitempty"`
	StorageClass string `xml:"StorageClass" json:"storageClass,omitempty"`
}

// Listing is one page of a folder: its files and subfolders.
type Listing struct {
	Objects    []Object `json:"objects"`
	Folders    []string `json:"folders"`
	NextMarker string   `json:"next,omitempty"`
}

// ListObjects lists up to max entries under prefix; with a delimiter of
// "/" it lists one folder level.
func (c *Client) ListObjects(ctx context.Context, bucket, region, prefix, delimiter, marker string, max int) (Listing, error) {
	q := url.Values{"max-keys": {strconv.Itoa(max)}, "encoding-type": {"url"}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if delimiter != "" {
		q.Set("delimiter", delimiter)
	}
	if marker != "" {
		q.Set("marker", marker)
	}
	resp, err := c.cosDo(ctx, cosRequest{method: http.MethodGet, bucket: bucket, region: region, query: q})
	if err != nil {
		return Listing{}, err
	}
	defer resp.Body.Close()
	var out struct {
		EncodingType string   `xml:"EncodingType"`
		IsTruncated  bool     `xml:"IsTruncated"`
		NextMarker   string   `xml:"NextMarker"`
		Contents     []Object `xml:"Contents"`
		Prefixes     []string `xml:"CommonPrefixes>Prefix"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&out); err != nil {
		return Listing{}, err
	}
	dec := func(s string) string {
		if out.EncodingType != "url" {
			return s
		}
		if d, err := url.QueryUnescape(s); err == nil {
			return d
		}
		return s
	}
	l := Listing{Objects: []Object{}, Folders: []string{}}
	for _, o := range out.Contents {
		o.Key = dec(o.Key)
		o.ETag = strings.Trim(o.ETag, `"`)
		l.Objects = append(l.Objects, o)
	}
	for _, p := range out.Prefixes {
		l.Folders = append(l.Folders, dec(p))
	}
	if out.IsTruncated {
		l.NextMarker = dec(out.NextMarker)
		if l.NextMarker == "" && len(out.Contents) > 0 {
			l.NextMarker = l.Objects[len(l.Objects)-1].Key
		}
	}
	return l, nil
}

// HeadObject reads a file's size and type; ok is false when it is not there.
func (c *Client) HeadObject(ctx context.Context, bucket, region, key string) (size int64, contentType string, ok bool, err error) {
	resp, err := c.cosDo(ctx, cosRequest{method: http.MethodHead, bucket: bucket, region: region, key: key})
	if err != nil {
		var e *COSError
		if errors.As(err, &e) && e.Status == http.StatusNotFound {
			return 0, "", false, nil
		}
		return 0, "", false, err
	}
	resp.Body.Close()
	return resp.ContentLength, resp.Header.Get("Content-Type"), true, nil
}

// PutObject uploads a file of the given size.
func (c *Client) PutObject(ctx context.Context, bucket, region, key string, body io.Reader, size int64, contentType string) error {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	if body == nil {
		body = bytes.NewReader(nil)
		size = 0
	}
	resp, err := c.cosDo(ctx, cosRequest{method: http.MethodPut, bucket: bucket, region: region, key: key, header: h, body: body, size: size})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// CopyObject copies a file within the region's buckets.
func (c *Client) CopyObject(ctx context.Context, bucket, region, from, to string) error {
	h := http.Header{}
	h.Set("x-cos-copy-source", COSHost(bucket, region)+"/"+cosEncode(from, "/"))
	resp, err := c.cosDo(ctx, cosRequest{method: http.MethodPut, bucket: bucket, region: region, key: to, header: h})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// A copy can fail after the 200 has been sent; the body says so.
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		XMLName xml.Name
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(data, &e) == nil && e.XMLName.Local == "Error" {
		return &COSError{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
	}
	return nil
}

// DeleteObject removes a file; a missing file is not an error.
func (c *Client) DeleteObject(ctx context.Context, bucket, region, key string) error {
	resp, err := c.cosDo(ctx, cosRequest{method: http.MethodDelete, bucket: bucket, region: region, key: key})
	if err != nil {
		if IsCOSCode(err, "NoSuchKey") {
			return nil
		}
		return err
	}
	resp.Body.Close()
	return nil
}

// DeleteObjects removes up to 1000 files at once and returns the keys it
// could not delete, with why.
func (c *Client) DeleteObjects(ctx context.Context, bucket, region string, keys []string) (map[string]string, error) {
	type obj struct {
		Key string `xml:"Key"`
	}
	req := struct {
		XMLName xml.Name `xml:"Delete"`
		Quiet   bool     `xml:"Quiet"`
		Objects []obj    `xml:"Object"`
	}{Quiet: true}
	for _, k := range keys {
		req.Objects = append(req.Objects, obj{k})
	}
	var out struct {
		Errors []struct {
			Key     string `xml:"Key"`
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	if err := c.cosCall(ctx, http.MethodPost, bucket, region, "delete", xmlBody(req), "", &out); err != nil {
		return nil, err
	}
	failed := map[string]string{}
	for _, e := range out.Errors {
		if e.Code != "NoSuchKey" {
			failed[e.Key] = (&COSError{Code: e.Code, Message: e.Message}).Error()
		}
	}
	return failed, nil
}

// PresignedURL is a link that works without credentials until it expires;
// with download set, the browser saves the file under its name.
func (c *Client) PresignedURL(bucket, region, key string, expires time.Duration, download bool) string {
	u, host, path := c.cosURL(bucket, region, key)
	q := url.Values{}
	if download {
		name := key[strings.LastIndex(key, "/")+1:]
		q.Set("response-content-disposition", "attachment; filename*=UTF-8''"+cosEncode(name, ""))
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	t := now()
	h := http.Header{}
	h.Set("Host", host)
	auth := COSAuthorization(c.SecretID, c.SecretKey, http.MethodGet, path, q, h, t.Add(-time.Minute), t.Add(expires))
	query := cosQuery(q)
	if query != "" {
		query += "&"
	}
	u.RawQuery = query + cosEncode(auth, "&=")
	return u.String()
}

// PublicURL is a file's plain address, which works when the bucket is
// public.
func PublicURL(bucket, region, key string) string {
	return "https://" + COSHost(bucket, region) + "/" + cosEncode(key, "/")
}

// ---- Usage, from Cloud Monitor ----

// MonitorDataDims reads one metric for the given dimensions.
func (c *Client) MonitorDataDims(ctx context.Context, region, namespace, metric string, dims map[string]string, period int, start, end string) ([]Point, error) {
	var d []map[string]string
	for k, v := range dims {
		d = append(d, map[string]string{"Name": k, "Value": v})
	}
	var out struct {
		DataPoints []struct {
			Timestamps []float64 `json:"Timestamps"`
			Values     []float64 `json:"Values"`
		} `json:"DataPoints"`
	}
	err := c.CallRegion(ctx, "monitor", monitorVersion, "GetMonitorData", region, map[string]any{
		"Namespace": namespace, "MetricName": metric, "Period": period, "StartTime": start, "EndTime": end,
		"Instances": []map[string]any{{"Dimensions": d}},
	}, &out)
	var pts []Point
	for _, dp := range out.DataPoints {
		for i, t := range dp.Timestamps {
			if i < len(dp.Values) {
				pts = append(pts, Point{T: int64(t), V: dp.Values[i]})
			}
		}
	}
	return pts, err
}
