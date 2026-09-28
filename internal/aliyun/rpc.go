// Package aliyun is a small hand-written client for the Alibaba Cloud
// (阿里云) APIs Miao Panel uses: SMS, ECS, Simple Application Server
// (轻量应用服务器), CloudMonitor, Alidns and CDN. They all speak the RPC
// style, signed with HMAC-SHA1 over the sorted, percent-encoded
// parameters. Actions, parameters and response fields were checked
// against the official SDKs and API metadata.
package aliyun

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

// Client holds an AccessKey. Keep Secret out of logs.
type Client struct {
	ID, Secret string
	// Endpoint is the SMS API's base URL; tests point it at a fake.
	Endpoint string
	// EndpointFor, when set, gives Call the base URL of a product in a
	// region; tests route everything to a fake. Defaults to
	// DefaultEndpoint.
	EndpointFor func(product, region string) string
	// Trace, when set, is told about every Call: the product, the action
	// (with @region) and the action's own parameters, never the key.
	Trace func(product, action string, params map[string]string)
	HTTP  *http.Client
	now   func() time.Time
}

// New makes a client. SMS goes to the mainland endpoint.
func New(id, secret string) *Client {
	return &Client{ID: id, Secret: secret, Endpoint: "https://dysmsapi.aliyuncs.com", HTTP: &http.Client{Timeout: 20 * time.Second}, now: time.Now}
}

// Products Call knows the endpoints of, and the API versions this
// package was written against.
const (
	ProductECS  = "ecs"       // 云服务器 ECS
	ProductSWAS = "swas-open" // 轻量应用服务器
	ProductCMS  = "cms"       // 云监控
	ProductDNS  = "alidns"    // 云解析 DNS
	ProductCDN  = "cdn"       // CDN

	VersionECS  = "2014-05-26"
	VersionSWAS = "2020-06-01"
	VersionCMS  = "2019-01-01"
	VersionDNS  = "2015-01-09"
	VersionCDN  = "2018-05-10"
)

// defaultRegion is where region-wide questions (which regions exist) go.
const defaultRegion = "cn-hangzhou"

// DefaultEndpoint is where a product's API lives: the regional products
// at a host per region, Alidns and CDN at one host for everything.
func DefaultEndpoint(product, region string) string {
	if region == "" {
		region = defaultRegion
	}
	switch product {
	case ProductECS:
		if region == "cn-hangzhou" {
			return "https://ecs-cn-hangzhou.aliyuncs.com" // what the official SDK uses for Hangzhou
		}
		return "https://ecs." + region + ".aliyuncs.com"
	case ProductSWAS:
		return "https://swas." + region + ".aliyuncs.com"
	case ProductCMS:
		return "https://metrics." + region + ".aliyuncs.com"
	case ProductDNS:
		return "https://alidns.aliyuncs.com"
	case ProductCDN:
		return "https://cdn.aliyuncs.com"
	case "dysmsapi":
		return "https://dysmsapi.aliyuncs.com"
	}
	return "https://" + product + "." + region + ".aliyuncs.com"
}

// PercentEncode is RFC 3986 encoding, as the signature wants it.
func PercentEncode(s string) string {
	s = url.QueryEscape(s)
	return strings.NewReplacer("+", "%20", "*", "%2A", "%7E", "~").Replace(s)
}

// StringToSign is the method, "/" and the sorted parameters, each encoded.
func StringToSign(method string, params map[string]string) string {
	return method + "&" + PercentEncode("/") + "&" + PercentEncode(encode(params))
}

// Signature signs the parameters with the AccessKey secret.
func Signature(method string, params map[string]string, secret string) string {
	m := hmac.New(sha1.New, []byte(secret+"&"))
	m.Write([]byte(StringToSign(method, params)))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// encode joins the parameters sorted by name, each percent-encoded.
func encode(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = PercentEncode(k) + "=" + PercentEncode(params[k])
	}
	return strings.Join(pairs, "&")
}

// Call runs one RPC action. region picks the endpoint and is sent as
// RegionId; leave it empty for the global products (Alidns, CDN). params
// are the action's own parameters, sent as a form body; the common ones
// (Action, Version, the signature…) go in the query string and the
// signature covers both, as the official SDKs do. The JSON reply is
// decoded into out.
func (c *Client) Call(ctx context.Context, product, version, action, region string, params map[string]string, out any) error {
	if c.Trace != nil {
		name := action
		if region != "" {
			name += "@" + region
		}
		cp := make(map[string]string, len(params))
		for k, v := range params {
			cp[k] = v
		}
		c.Trace(product, name, cp)
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	common := map[string]string{
		"Action": action, "Version": version, "Format": "JSON", "AccessKeyId": c.ID,
		"SignatureMethod": "HMAC-SHA1", "SignatureVersion": "1.0", "SignatureNonce": hex.EncodeToString(nonce),
		"Timestamp": now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	if region != "" {
		common["RegionId"] = region
	}
	body := map[string]string{}
	signed := map[string]string{}
	for k, v := range params {
		if _, clash := common[k]; !clash {
			body[k], signed[k] = v, v
		}
	}
	for k, v := range common {
		signed[k] = v
	}
	common["Signature"] = Signature(http.MethodPost, signed, c.Secret)
	base := DefaultEndpoint(product, region)
	if c.EndpointFor != nil {
		base = c.EndpointFor(product, region)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/?"+encode(common), strings.NewReader(encode(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		// Not the URL: it carries the key ID and the signature.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("连不上阿里云（%s）：%v", productName(product), err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("读取阿里云（%s）的回复时出错：%v", productName(product), err)
	}
	var head struct {
		RequestID string `json:"RequestId"`
		Code      text   `json:"Code"`
		Message   text   `json:"Message"`
		Success   text   `json:"Success"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("阿里云（%s）返回了看不懂的内容（HTTP %d）：%s", productName(product), resp.StatusCode, msg)
	}
	// CloudMonitor answers some refusals with HTTP 200 and Success false.
	if resp.StatusCode >= 300 || head.Success == "false" {
		code := string(head.Code)
		if code == "" {
			code = "HTTP" + strconv.Itoa(resp.StatusCode)
		}
		return &APIError{Product: product, Action: action, Code: code, Message: string(head.Message), RequestID: head.RequestID, HTTPStatus: resp.StatusCode}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("阿里云（%s）%s 的回复格式和预期不一样：%v", productName(product), action, err)
		}
	}
	return nil
}

// APIError is a refusal from an Alibaba Cloud API other than SMS.
type APIError struct {
	Product, Action string
	Code, Message   string
	RequestID       string
	HTTPStatus      int
}

func (e *APIError) Error() string {
	name := productName(e.Product)
	switch {
	case e.Code == "InvalidAccessKeyId.NotFound" || e.Code == "InvalidAccessKeyId":
		return "阿里云 AccessKey ID 不对（阿里云找不到这个 AccessKey），请重新复制"
	case e.Code == "InvalidAccessKeyId.Inactive" || e.Code == "Forbidden.AccessKeyDisabled":
		return "这个阿里云 AccessKey 已经被禁用了，请在 RAM 访问控制里启用它，或者换一个"
	case e.Code == "SignatureDoesNotMatch" || e.Code == "IncompleteSignature":
		return "阿里云 AccessKey Secret 不对，请重新复制"
	case strings.HasPrefix(e.Code, "InvalidTimeStamp"):
		return "电脑的时间不准，阿里云拒绝了请求，请把时间校准后再试"
	case denied(e.Code):
		msg := fmt.Sprintf("这个阿里云 AccessKey 没有 %s 的权限", name)
		if p := Policy(e.Product); p != "" {
			msg += fmt.Sprintf("，请在 RAM 访问控制里给它授权 %s", p)
		}
		if e.Message != "" {
			msg += "（阿里云原话：" + e.Message + "）"
		}
		return msg
	case strings.HasPrefix(e.Code, "Throttling"):
		return fmt.Sprintf("阿里云（%s）说请求太频繁了，请稍等一会儿再试", name)
	}
	if h := hintFor(e.Code); h != "" {
		return fmt.Sprintf("阿里云（%s）拒绝了操作：%s（%s）", name, h, e.Code)
	}
	return fmt.Sprintf("阿里云（%s）返回错误：%s（%s）", name, e.Message, e.Code)
}

// denied reports the codes Alibaba Cloud uses when RAM does not allow
// the call. CloudMonitor sometimes says just "403".
func denied(code string) bool {
	switch code {
	case "Forbidden.RAM", "NoPermission", "Forbidden.NoPermission", "Forbidden", "Forbidden.Unauthorized", "403":
		return true
	}
	return strings.HasPrefix(code, "Forbidden.RAM") || strings.HasPrefix(code, "NoPermission")
}

// hintFor explains, in words, refusals people run into in normal use.
func hintFor(code string) string {
	switch code {
	case "IncorrectInstanceStatus":
		return "服务器当前的状态不能做这个操作（比如已经在运行的不能再开机），稍等一会儿再试"
	case "DomainRecordDuplicate":
		return "已经有一条完全一样的解析记录了"
	case "DomainRecordConflict":
		return "和已有的解析记录冲突（比如同一个主机记录下 CNAME 不能和 A、AAAA、MX 等记录共存）"
	case "IncorrectDomainUser", "InvalidDomainName.NoExist":
		return "这个域名不在这个阿里云账号的云解析 DNS 里"
	}
	return ""
}

// Policy is the RAM system policy to grant an AccessKey so that it can
// use a product.
func Policy(product string) string {
	switch product {
	case ProductECS:
		return "AliyunECSFullAccess"
	case ProductSWAS:
		return "AliyunSWASFullAccess"
	case ProductCMS:
		return "AliyunCloudMonitorReadOnlyAccess"
	case ProductDNS:
		return "AliyunDNSFullAccess"
	case ProductCDN:
		return "AliyunCDNFullAccess"
	case "dysmsapi":
		return "AliyunDysmsFullAccess"
	}
	return ""
}

func productName(product string) string {
	switch product {
	case ProductECS:
		return "云服务器 ECS"
	case ProductSWAS:
		return "轻量应用服务器"
	case ProductCMS:
		return "云监控"
	case ProductDNS:
		return "云解析 DNS"
	case ProductCDN:
		return "CDN"
	}
	return product
}

// IsCode reports whether err is an API refusal whose code starts with
// prefix.
func IsCode(err error, prefix string) bool {
	var e *APIError
	return errors.As(err, &e) && strings.HasPrefix(e.Code, prefix)
}

// text decodes a JSON string, number or boolean as a string: the APIs
// are not consistent about which one a field is.
type text string

func (t *text) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*t = ""
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = text(s)
		return nil
	}
	*t = text(strings.TrimSpace(string(b)))
	return nil
}

// number decodes a JSON number or a string holding one; anything else
// is zero.
type number float64

func (n *number) UnmarshalJSON(b []byte) error {
	var t text
	_ = t.UnmarshalJSON(b)
	f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(string(t)), "%"), 64)
	if err != nil {
		f = 0
	}
	*n = number(f)
	return nil
}

// jsonList writes IDs as the JSON array the "…Ids" parameters take.
func jsonList(ids []string) string {
	b, _ := json.Marshal(ids)
	return string(b)
}

// rfc3339 rewrites the API's ISO 8601 times (some without seconds) as
// RFC 3339 in UTC; what it cannot read it returns as is.
func rfc3339(s string) string {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04Z", "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return s
}
