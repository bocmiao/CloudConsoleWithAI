package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Messages (the daily report and alerts) can be pushed to a group robot
// or a push service. The kind is read from the address the user pastes.

const (
	hookWeCom      = "wecom"
	hookDingTalk   = "dingtalk"
	hookFeishu     = "feishu"
	hookServerChan = "serverchan"
	hookGeneric    = "generic"
)

var hookNames = map[string]string{
	hookWeCom: "企业微信群机器人", hookDingTalk: "钉钉群机器人", hookFeishu: "飞书群机器人",
	hookServerChan: "Server酱（推送到微信）", hookGeneric: "通用 Webhook（POST JSON）",
}

var serverChanRe = regexp.MustCompile(`/SCT[0-9A-Za-z]+\.send$|ftqq\.com/`)

// webhookKind tells the service by its address.
func webhookKind(u string) string {
	switch {
	case strings.Contains(u, "/cgi-bin/webhook/send"):
		return hookWeCom
	case strings.Contains(u, "/robot/send"):
		return hookDingTalk
	case strings.Contains(u, "/open-apis/bot/"):
		return hookFeishu
	case serverChanRe.MatchString(strings.SplitN(u, "?", 2)[0]):
		return hookServerChan
	}
	return hookGeneric
}

// maskURL hides the key in an address: the token, the key parameter or
// the last part of the path.
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "（已保存）"
	}
	q := u.Query()
	for k := range q {
		q.Set(k, "…")
	}
	u.RawQuery = q.Encode()
	if i := strings.LastIndex(u.Path, "/"); i >= 0 && len(u.Path)-i > 8 {
		u.Path = u.Path[:i+1] + "…"
	}
	s, _ := url.QueryUnescape(u.String())
	return s
}

// clipBytes cuts text to at most n bytes of whole characters.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n……"
}

func hmacB64(key, msg string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(msg))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// sendWebhook posts a message; text is simple Markdown (bold, lists).
func sendWebhook(ctx context.Context, client *http.Client, hook, secret, title, text string) error {
	kind := webhookKind(hook)
	var body []byte
	contentType := "application/json"
	target := hook
	switch kind {
	case hookWeCom: // markdown content is limited to 4096 bytes
		body, _ = json.Marshal(map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": clipBytes("**"+title+"**\n"+text, 4000)}})
	case hookDingTalk:
		if secret != "" {
			ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target += sep + "timestamp=" + ts + "&sign=" + url.QueryEscape(hmacB64(secret, ts+"\n"+secret))
		}
		body, _ = json.Marshal(map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": title, "text": clipBytes("### "+title+"\n"+text, 18000)}})
	case hookFeishu:
		msg := map[string]any{"msg_type": "interactive", "card": map[string]any{
			"header":   map[string]any{"title": map[string]string{"tag": "plain_text", "content": title}},
			"elements": []any{map[string]string{"tag": "markdown", "content": clipBytes(text, 18000)}},
		}}
		if secret != "" {
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			msg["timestamp"], msg["sign"] = ts, hmacB64(ts+"\n"+secret, "")
		}
		body, _ = json.Marshal(msg)
	case hookServerChan:
		form := url.Values{"title": {clipBytes(title, 90)}, "desp": {text}}
		body, contentType = []byte(form.Encode()), "application/x-www-form-urlencoded"
	default:
		body, _ = json.Marshal(map[string]string{"title": title, "text": text, "source": "Miao Panel"})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("推送地址不对：%v", err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("连不上推送地址：%v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("推送失败（HTTP %d）：%s", resp.StatusCode, clipBytes(strings.TrimSpace(string(data)), 200))
	}
	if kind == hookGeneric {
		return nil
	}
	// The robots answer 200 with an error code in the body.
	var r struct {
		ErrCode    *int   `json:"errcode"`
		Code       *int   `json:"code"`
		StatusCode *int   `json:"StatusCode"`
		ErrMsg     string `json:"errmsg"`
		Msg        string `json:"msg"`
		Message    string `json:"message"`
	}
	if json.Unmarshal(data, &r) != nil {
		return nil
	}
	for _, c := range []*int{r.ErrCode, r.Code, r.StatusCode} {
		if c != nil && *c != 0 {
			msg := r.ErrMsg + r.Msg + r.Message
			return fmt.Errorf("推送失败（%d）：%s", *c, clipBytes(msg, 200))
		}
	}
	return nil
}
