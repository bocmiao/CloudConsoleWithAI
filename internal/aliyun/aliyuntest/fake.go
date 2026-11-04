// Package aliyuntest has stand-ins for the Alibaba Cloud APIs Miao Panel
// uses: Fake for SMS, which keeps what it was asked to send, and Cloud
// for ECS, Simple Application Server, CloudMonitor, Alidns and CDN. Both
// check each request's signature.
package aliyuntest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
)

// The AccessKey the fakes accept.
const (
	ID     = "LTAI5tTestKeyId000000"
	Secret = "TestSecretForTheFake0000000000"
)

// Sent is a message the fake accepted.
type Sent struct {
	Phone, Sign, Template string
	Params                map[string]string
}

// Fake answers SendSms.
type Fake struct {
	URL string
	// The approved signature, template and variable names it accepts.
	Sign, Template string
	Vars           []string

	mu   sync.Mutex
	Sent []Sent
}

// Start runs a fake with a signature 喵面板 and a template
// SMS_123456789 taking ${code}.
func Start(t *testing.T) *Fake {
	f := &Fake{Sign: "喵面板", Template: "SMS_123456789", Vars: []string{"code"}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *Fake) answer(w http.ResponseWriter, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"Code": code, "Message": msg, "RequestId": "fake"})
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := map[string]string{}
	for k, v := range r.URL.Query() {
		if k != "Signature" {
			q[k] = v[0]
		}
	}
	switch {
	case q["AccessKeyId"] != ID:
		f.answer(w, "InvalidAccessKeyId.NotFound", "Specified access key is not found.")
		return
	case r.URL.Query().Get("Signature") != aliyun.Signature(r.Method, q, Secret):
		f.answer(w, "SignatureDoesNotMatch", "Specified signature is not matched with our calculation.")
		return
	case q["Action"] != "SendSms" || q["Version"] != "2017-05-25":
		f.answer(w, "InvalidAction.NotFound", "unknown action")
		return
	case q["SignName"] != f.Sign:
		f.answer(w, "isv.SMS_SIGNATURE_ILLEGAL", "签名不合法")
		return
	case q["TemplateCode"] != f.Template:
		f.answer(w, "isv.SMS_TEMPLATE_ILLEGAL", "模板不合法")
		return
	}
	var params map[string]string
	if json.Unmarshal([]byte(q["TemplateParam"]), &params) != nil {
		f.answer(w, "isv.INVALID_JSON_PARAM", "JSON参数不合法")
		return
	}
	for _, v := range f.Vars {
		if params[v] == "" {
			f.answer(w, "isv.TEMPLATE_MISSING_PARAMETERS", "模板缺少变量")
			return
		}
	}
	f.mu.Lock()
	f.Sent = append(f.Sent, Sent{Phone: q["PhoneNumbers"], Sign: q["SignName"], Template: q["TemplateCode"], Params: params})
	f.mu.Unlock()
	f.answer(w, "OK", "OK")
}
