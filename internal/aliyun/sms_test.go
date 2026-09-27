package aliyun_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
)

// Vectors from Alibaba Cloud's own signing code (openapi-util v0.1.1
// GetRPCSignature), the first being its published test.
func TestSignatureMatchesAlibabaCloud(t *testing.T) {
	for _, c := range []struct {
		method, secret string
		p              map[string]string
		want           string
	}{
		{"", "accessKeySecret", map[string]string{"test": "ok"}, "jHx/oHoHNrbVfhncHEvPdHXZwHU="},
		{"GET", "testSecret", map[string]string{"Action": "SendSms", "Version": "2017-05-25", "Format": "JSON", "RegionId": "cn-hangzhou",
			"AccessKeyId": "testId", "SignatureMethod": "HMAC-SHA1", "SignatureVersion": "1.0", "SignatureNonce": "45e25e9b-0a6f-4070-8c85-2956eda1b466",
			"Timestamp": "2017-07-12T02:42:19Z", "PhoneNumbers": "15300000001", "SignName": "阿里云短信测试专用", "TemplateCode": "SMS_71390007",
			"TemplateParam": `{"customer":"test"}`, "OutId": "123"}, "bkmmeClMQy7131fLU1mHu+mlly8="},
		{"GET", "s3cr3t/+=", map[string]string{"SignName": "喵 面板*~", "TemplateParam": `{"code":"123456","minutes":"10"}`, "A": "a b+c", "Z": "~*'()!"}, "NZT2/81CvHoV7j/pkknpHUtMXQ8="},
	} {
		if got := aliyun.Signature(c.method, c.p, c.secret); got != c.want {
			t.Errorf("signature %s, want %s", got, c.want)
		}
	}
}

func TestSendSMS(t *testing.T) {
	f := aliyuntest.Start(t)
	c := aliyun.New(aliyuntest.ID, aliyuntest.Secret)
	c.Endpoint = f.URL
	ctx := context.Background()
	if err := c.SendSMS(ctx, "喵面板", "SMS_123456789", "+8613800138000", map[string]string{"code": "654321"}); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 1 || f.Sent[0].Phone != "13800138000" || f.Sent[0].Params["code"] != "654321" {
		t.Fatalf("sent %+v", f.Sent)
	}
	if err := c.SendSMS(ctx, "喵面板", "SMS_123456789", "+85291234567", map[string]string{"code": "1"}); err != nil || f.Sent[1].Phone != "85291234567" {
		t.Fatalf("abroad: %v %+v", err, f.Sent)
	}
	for _, x := range []struct {
		sign, tpl string
		params    map[string]string
		want      string
	}{
		{"别人的签名", "SMS_123456789", map[string]string{"code": "1"}, "签名"},
		{"喵面板", "SMS_000", map[string]string{"code": "1"}, "模板 CODE"},
		{"喵面板", "SMS_123456789", map[string]string{"number": "1"}, "变量名"},
	} {
		if err := c.SendSMS(ctx, x.sign, x.tpl, "+8613800138000", x.params); err == nil || !strings.Contains(err.Error(), x.want) {
			t.Errorf("%+v: %v", x, err)
		}
	}
	bad := aliyun.New(aliyuntest.ID, "wrong secret")
	bad.Endpoint = f.URL
	if err := bad.SendSMS(ctx, "喵面板", "SMS_123456789", "+8613800138000", map[string]string{"code": "1"}); err == nil || !strings.Contains(err.Error(), "Secret 不对") {
		t.Fatalf("wrong secret: %v", err)
	}
}

// A network failure does not repeat the request's URL.
func TestSendSMSUnreachable(t *testing.T) {
	c := aliyun.New(aliyuntest.ID, aliyuntest.Secret)
	c.Endpoint = "http://127.0.0.1:1"
	err := c.SendSMS(context.Background(), "喵面板", "SMS_123456789", "+8613800138000", map[string]string{"code": "1"})
	if err == nil || strings.Contains(err.Error(), "13800138000") || strings.Contains(err.Error(), aliyuntest.ID) {
		t.Fatalf("error = %v", err)
	}
}
