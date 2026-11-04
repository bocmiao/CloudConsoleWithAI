package aliyun_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
)

func TestDefaultEndpoints(t *testing.T) {
	for _, c := range []struct{ product, region, want string }{
		{aliyun.ProductECS, "cn-hangzhou", "https://ecs-cn-hangzhou.aliyuncs.com"},
		{aliyun.ProductECS, "cn-beijing", "https://ecs.cn-beijing.aliyuncs.com"},
		{aliyun.ProductSWAS, "cn-shanghai", "https://swas.cn-shanghai.aliyuncs.com"},
		{aliyun.ProductSWAS, "", "https://swas.cn-hangzhou.aliyuncs.com"},
		{aliyun.ProductCMS, "ap-southeast-1", "https://metrics.ap-southeast-1.aliyuncs.com"},
		{aliyun.ProductDNS, "", "https://alidns.aliyuncs.com"},
		{aliyun.ProductCDN, "", "https://cdn.aliyuncs.com"},
	} {
		if got := aliyun.DefaultEndpoint(c.product, c.region); got != c.want {
			t.Errorf("DefaultEndpoint(%s, %s) = %s, want %s", c.product, c.region, got, c.want)
		}
	}
}

// The fake checks the signature, key, clock, version, action and
// parameters like the gateway; each refusal comes back typed and in words.
func TestCallRefusals(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	ctx := context.Background()
	c := f.Client()
	var traced []string
	c.Trace = func(product, action string, params map[string]string) {
		traced = append(traced, product+" "+action)
		for k, v := range params {
			if strings.Contains(k+v, aliyuntest.Secret) || strings.Contains(k+v, aliyuntest.ID) {
				t.Errorf("trace leaks the key: %s=%s", k, v)
			}
		}
	}
	regions, err := c.Regions(ctx, aliyun.KindECS)
	if err != nil || len(regions) != 3 || regions[0].ID != "cn-hangzhou" || regions[0].Name != "华东1（杭州）" {
		t.Fatalf("regions = %+v, %v", regions, err)
	}
	if len(traced) != 1 || traced[0] != "ecs DescribeRegions@cn-hangzhou" {
		t.Fatalf("trace = %v", traced)
	}

	bad := aliyun.New(aliyuntest.ID, "wrong secret")
	bad.EndpointFor = f.Endpoint
	_, err = bad.Regions(ctx, aliyun.KindSWAS)
	var ae *aliyun.APIError
	if !errors.As(err, &ae) || ae.Code != "SignatureDoesNotMatch" || ae.HTTPStatus != 400 || !strings.Contains(err.Error(), "Secret 不对") {
		t.Fatalf("wrong secret: %v", err)
	}
	unknown := aliyun.New("LTAI5tSomeoneElse", aliyuntest.Secret)
	unknown.EndpointFor = f.Endpoint
	if _, err := unknown.Domains(ctx); !aliyun.IsCode(err, "InvalidAccessKeyId.NotFound") || !strings.Contains(err.Error(), "AccessKey ID 不对") {
		t.Fatalf("unknown key: %v", err)
	}

	f.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := c.Domains(ctx); !aliyun.IsCode(err, "InvalidTimeStamp.Expired") || !strings.Contains(err.Error(), "时间不准") {
		t.Fatalf("clock: %v", err)
	}
	f.Now = time.Now

	f.Denied = map[string]bool{aliyun.ProductCDN: true}
	if _, err := c.CDNDomains(ctx); !aliyun.IsCode(err, "Forbidden.RAM") || !strings.Contains(err.Error(), "AliyunCDNFullAccess") {
		t.Fatalf("denied: %v", err)
	}

	for _, x := range []struct {
		version, action string
		params          map[string]string
		code            string
	}{
		{aliyun.VersionECS, "DescribeEverything", nil, "InvalidAction.NotFound"},
		{"2016-03-14", "DescribeInstances", nil, "InvalidVersion"},
		{aliyun.VersionECS, "DescribeSecurityGroupAttribute", nil, "MissingSecurityGroupId"},
		{aliyun.VersionECS, "DescribeInstances", map[string]string{"InstanceID": "i-1"}, "UnknownParameter"},
		{aliyun.VersionECS, "DescribeInstances", map[string]string{"MaxResults": "500"}, "InvalidParameter"},
	} {
		err := c.Call(ctx, aliyun.ProductECS, x.version, x.action, "cn-hangzhou", x.params, nil)
		if !aliyun.IsCode(err, x.code) {
			t.Errorf("%s %v: %v, want %s", x.action, x.params, err, x.code)
		}
	}
	// A regional call must go to its region's endpoint.
	c.EndpointFor = func(product, region string) string { return f.Endpoint(product, "cn-shanghai") }
	if _, _, err := c.Instance(ctx, "cn-hangzhou", "i-bp1shop000000000001"); !aliyun.IsCode(err, "WrongEndpoint") {
		t.Fatalf("wrong endpoint: %v", err)
	}
}

// CloudMonitor can refuse with HTTP 200 and Success false.
func TestCallSuccessFalse(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	c := f.Client()
	now := time.Now()
	_, err := c.CloudMonitor(context.Background(), "cn-hangzhou", "acs_nothing", "cpu", map[string]string{"instanceId": "i-1"}, 60, now.Add(-time.Hour), now)
	var ae *aliyun.APIError
	if !errors.As(err, &ae) || ae.Code != "404" || ae.HTTPStatus != 200 || !strings.Contains(err.Error(), "云监控") {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorWords(t *testing.T) {
	for _, c := range []struct {
		err  aliyun.APIError
		want string
	}{
		{aliyun.APIError{Product: aliyun.ProductECS, Code: "Forbidden.RAM", Message: "User not authorized"}, "AliyunECSFullAccess"},
		{aliyun.APIError{Product: aliyun.ProductSWAS, Code: "NoPermission"}, "AliyunSWASFullAccess"},
		{aliyun.APIError{Product: aliyun.ProductDNS, Code: "Forbidden.RAM"}, "AliyunDNSFullAccess"},
		{aliyun.APIError{Product: aliyun.ProductCMS, Code: "403"}, "AliyunCloudMonitorReadOnlyAccess"},
		{aliyun.APIError{Product: aliyun.ProductCDN, Code: "Throttling.User"}, "太频繁"},
		{aliyun.APIError{Product: aliyun.ProductECS, Code: "InvalidAccessKeyId.Inactive"}, "禁用"},
		{aliyun.APIError{Product: aliyun.ProductECS, Code: "IncorrectInstanceStatus"}, "当前的状态"},
		{aliyun.APIError{Product: aliyun.ProductECS, Code: "Something.New", Message: "odd"}, "odd（Something.New）"},
	} {
		if got := c.err.Error(); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q lacks %q", c.err.Code, got, c.want)
		}
	}
}

// A network failure does not repeat the request's URL.
func TestCallUnreachable(t *testing.T) {
	c := aliyun.New(aliyuntest.ID, aliyuntest.Secret)
	c.EndpointFor = func(string, string) string { return "http://127.0.0.1:1" }
	_, err := c.Regions(context.Background(), aliyun.KindECS)
	if err == nil || strings.Contains(err.Error(), aliyuntest.ID) || strings.Contains(err.Error(), "Signature") || !strings.Contains(err.Error(), "连不上阿里云") {
		t.Fatalf("error = %v", err)
	}
}
