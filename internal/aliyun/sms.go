// Package aliyun sends text messages through Alibaba Cloud SMS (阿里云短信
// 服务, dysmsapi), signed the way Alibaba Cloud's RPC APIs are: HMAC-SHA1
// over the sorted, percent-encoded parameters.
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
	"strings"
	"time"
)

// Client calls dysmsapi with an AccessKey.
type Client struct {
	ID, Secret string
	// Endpoint is the API's base URL; tests point it at a fake.
	Endpoint string
	HTTP     *http.Client
	now      func() time.Time
}

// New makes a client for the mainland endpoint.
func New(id, secret string) *Client {
	return &Client{ID: id, Secret: secret, Endpoint: "https://dysmsapi.aliyuncs.com", HTTP: &http.Client{Timeout: 20 * time.Second}, now: time.Now}
}

// PercentEncode is RFC 3986 encoding, as the signature wants it.
func PercentEncode(s string) string {
	s = url.QueryEscape(s)
	return strings.NewReplacer("+", "%20", "*", "%2A", "%7E", "~").Replace(s)
}

// StringToSign is the method, "/" and the sorted parameters, each encoded.
func StringToSign(method string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = PercentEncode(k) + "=" + PercentEncode(params[k])
	}
	return method + "&" + PercentEncode("/") + "&" + PercentEncode(strings.Join(pairs, "&"))
}

// Signature signs the parameters with the AccessKey secret.
func Signature(method string, params map[string]string, secret string) string {
	m := hmac.New(sha1.New, []byte(secret+"&"))
	m.Write([]byte(StringToSign(method, params)))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// Error is a refusal from the API, with its code.
type Error struct {
	Code, Message string
}

func (e *Error) Error() string {
	if h := hint(e.Code); h != "" {
		return fmt.Sprintf("阿里云短信没有发出（%s）：%s", e.Code, h)
	}
	return fmt.Sprintf("阿里云短信没有发出（%s）：%s", e.Code, e.Message)
}

// hint says in words what the common refusals mean.
func hint(code string) string {
	switch code {
	case "isv.SMS_SIGNATURE_ILLEGAL", "isv.SMS_SIGN_ILLEGAL", "isv.SIGN_NAME_ILLEGAL":
		return "签名不对或还没审核通过"
	case "isv.SMS_TEMPLATE_ILLEGAL", "isv.TEMPLATE_PARAMS_ILLEGAL":
		return "模板 CODE 不对、还没审核通过，或者和签名不匹配"
	case "isv.TEMPLATE_MISSING_PARAMETERS", "isv.INVALID_JSON_PARAM":
		return "模板里的变量名和设置的不一样"
	case "isv.MOBILE_NUMBER_ILLEGAL":
		return "手机号格式不对"
	case "isv.BUSINESS_LIMIT_CONTROL", "isv.DAY_LIMIT_CONTROL":
		return "这个号码发得太频繁，被阿里云限制了，稍后再试"
	case "isv.AMOUNT_NOT_ENOUGH", "isv.OUT_OF_SERVICE":
		return "账户余额不足或者欠费停机了"
	case "InvalidAccessKeyId.NotFound", "SignatureDoesNotMatch", "IncompleteSignature":
		return "AccessKey ID 或 Secret 不对"
	case "isp.RAM_PERMISSION_DENY", "Forbidden.RAM":
		return "这个 AccessKey 没有发短信的权限（RAM 用户要有 AliyunDysmsFullAccess）"
	case "isv.ACCOUNT_NOT_EXISTS", "isv.PRODUCT_UN_SUBSCRIPT":
		return "账号还没有开通短信服务"
	}
	return ""
}

// Phone writes a number the way dysmsapi wants it: mainland numbers
// without +86, others with the country code but no +.
func Phone(e164 string) string {
	if strings.HasPrefix(e164, "+86") {
		return e164[3:]
	}
	return strings.TrimPrefix(e164, "+")
}

// SendSMS sends one templated message. params are the template's
// variables, e.g. {"code": "123456"}.
func (c *Client) SendSMS(ctx context.Context, sign, template, phone string, params map[string]string) error {
	tp, _ := json.Marshal(params)
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	q := map[string]string{
		"Action": "SendSms", "Version": "2017-05-25", "Format": "JSON", "RegionId": "cn-hangzhou",
		"AccessKeyId": c.ID, "SignatureMethod": "HMAC-SHA1", "SignatureVersion": "1.0",
		"SignatureNonce": hex.EncodeToString(nonce), "Timestamp": now().UTC().Format("2006-01-02T15:04:05Z"),
		"PhoneNumbers": Phone(phone), "SignName": sign, "TemplateCode": template, "TemplateParam": string(tp),
	}
	sig := Signature(http.MethodGet, q, c.Secret)
	pairs := []string{"Signature=" + PercentEncode(sig)}
	for k, v := range q {
		pairs = append(pairs, PercentEncode(k)+"="+PercentEncode(v))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Endpoint, "/")+"/?"+strings.Join(pairs, "&"), nil)
	if err != nil {
		return err
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		// Not the URL: it carries the key ID, the number and the signature.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("连不上阿里云短信服务：%v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var out struct{ Code, Message string }
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("阿里云短信返回的内容看不懂（HTTP %d）", resp.StatusCode)
	}
	if out.Code != "OK" {
		return &Error{Code: out.Code, Message: out.Message}
	}
	return nil
}
