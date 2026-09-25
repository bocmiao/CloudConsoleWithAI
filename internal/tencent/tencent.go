// Package tencent calls Tencent Cloud API 3.0 (DNSPod, EdgeOne) with
// TC3-HMAC-SHA256 signatures. It is a small hand-written client: only the
// calls Miao Panel uses, with request and response fields checked against
// the official SDK.
package tencent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client holds API credentials. Keep SecretKey out of logs.
type Client struct {
	SecretID  string
	SecretKey string
	// Endpoint returns the base URL for a service; tests point it at a
	// fake. Defaults to https://<service>.tencentcloudapi.com.
	Endpoint func(service string) string
	// Trace, when set, is told about every call (never the credentials).
	Trace func(service, action string, body []byte)
	hc    *http.Client
	now   func() time.Time
}

// New creates a client.
func New(secretID, secretKey string) *Client {
	return &Client{
		SecretID: strings.TrimSpace(secretID), SecretKey: strings.TrimSpace(secretKey),
		hc: &http.Client{Timeout: 60 * time.Second}, now: time.Now,
	}
}

// Error is an error returned by the API.
type Error struct {
	Service   string
	Code      string
	Message   string
	RequestID string
}

func (e *Error) Error() string { return friendly(e) }

// friendly explains the errors people run into when setting up access.
func friendly(e *Error) string {
	switch {
	case strings.HasPrefix(e.Code, "AuthFailure.SecretIdNotFound"), strings.HasPrefix(e.Code, "AuthFailure.SignatureFailure"),
		e.Code == "AuthFailure.InvalidSecretId":
		return "腾讯云 SecretId 或 SecretKey 不对，请重新复制"
	case strings.HasPrefix(e.Code, "AuthFailure.SignatureExpire"):
		return "电脑的时间不准，腾讯云拒绝了请求，请把电脑时间校准后再试"
	case strings.HasPrefix(e.Code, "AuthFailure.UnauthorizedOperation"), strings.HasPrefix(e.Code, "UnauthorizedOperation"):
		name := map[string]string{"dnspod": "DNSPod", "teo": "EdgeOne", "lighthouse": "轻量应用服务器", "cvm": "云服务器 CVM",
			"vpc": "私有网络（安全组）", "cbs": "云硬盘（快照）", "monitor": "云监控", "tat": "自动化助手（TAT）"}[e.Service]
		if name == "" {
			name = e.Service
		}
		return fmt.Sprintf("这个腾讯云子账号没有 %s 的权限，请在访问管理里给它授权（%s）", name, e.Message)
	case e.Code == "ResourceUnavailable.AgentNotInstalled":
		return "这台服务器没有安装腾讯云自动化助手（TAT）"
	case e.Code == "ResourceUnavailable.AgentStatusNotOnline":
		return "这台服务器的腾讯云自动化助手（TAT）不在线：请确认服务器正在运行，并且 tat_agent 服务正常"
	case e.Code == "ResourceUnavailable.InstanceStateNotRunning":
		return "这台服务器没有在运行"
	}
	return fmt.Sprintf("腾讯云返回错误：%s（%s）", e.Message, e.Code)
}

func hmacSHA256(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Authorization computes the TC3-HMAC-SHA256 Authorization header for a
// POST with a JSON body, signing the content-type and host headers.
func Authorization(secretID, secretKey, service, host string, timestamp int64, body []byte) string {
	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	canonical := "POST\n/\n\ncontent-type:application/json\nhost:" + host + "\n\ncontent-type;host\n" + sha256hex(body)
	scope := date + "/" + service + "/tc3_request"
	toSign := "TC3-HMAC-SHA256\n" + strconv.FormatInt(timestamp, 10) + "\n" + scope + "\n" + sha256hex([]byte(canonical))
	key := hmacSHA256(hmacSHA256(hmacSHA256([]byte("TC3"+secretKey), date), service), "tc3_request")
	return "TC3-HMAC-SHA256 Credential=" + secretID + "/" + scope +
		", SignedHeaders=content-type;host, Signature=" + hex.EncodeToString(hmacSHA256(key, toSign))
}

// Call runs one API action of a global service. in is marshalled as the
// request body; the "Response" object of the reply is decoded into out.
func (c *Client) Call(ctx context.Context, service, version, action string, in, out any) error {
	return c.CallRegion(ctx, service, version, action, "", in, out)
}

// CallRegion runs one API action in a region (e.g. ap-guangzhou).
func (c *Client) CallRegion(ctx context.Context, service, version, action, region string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if c.Trace != nil {
		name := action
		if region != "" {
			name += "@" + region
		}
		c.Trace(service, name, body)
	}
	base := "https://" + service + ".tencentcloudapi.com"
	if c.Endpoint != nil {
		base = c.Endpoint(service)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	host := req.URL.Host
	ts := c.now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", version)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-TC-Language", "zh-CN")
	if region != "" {
		req.Header.Set("X-TC-Region", region)
	}
	req.Header.Set("Authorization", Authorization(c.SecretID, c.SecretKey, service, host, ts, body))
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("连不上腾讯云（%s）：%w", service, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	var env struct {
		Response json.RawMessage `json:"Response"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Response) == 0 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("腾讯云返回了无法识别的内容（HTTP %d）：%s", resp.StatusCode, msg)
	}
	var head struct {
		RequestID string `json:"RequestId"`
		Error     *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := json.Unmarshal(env.Response, &head); err != nil {
		return err
	}
	if head.Error != nil {
		return &Error{Service: service, Code: head.Error.Code, Message: head.Error.Message, RequestID: head.RequestID}
	}
	if out != nil {
		return json.Unmarshal(env.Response, out)
	}
	return nil
}

// IsCode reports whether err is an API error whose code starts with prefix.
func IsCode(err error, prefix string) bool {
	e, ok := err.(*Error)
	return ok && strings.HasPrefix(e.Code, prefix)
}
