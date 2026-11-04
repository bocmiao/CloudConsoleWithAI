package aliyuntest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
)

// Cloud is a stand-in for the ECS, Simple Application Server,
// CloudMonitor, Alidns and CDN APIs. It checks every request the way the
// API gateway does (key, signature, nonce, timestamp, version, action,
// required parameters, and that the call went to its region's endpoint),
// refuses parameters those actions do not have, keeps state, and answers
// in the real response shapes.
type Cloud struct {
	URL string
	// PageSize, when set, caps every page the fake answers with, to test
	// paging.
	PageSize int
	// Denied products ("ecs", "swas-open", …) answer every call with
	// Forbidden.RAM.
	Denied map[string]bool
	// FailRegions makes every call to a "product region" (e.g. "ecs
	// cn-beijing") fail with InternalError.
	FailRegions map[string]bool
	// Now is the clock the fake checks timestamps against.
	Now func() time.Time

	mu     sync.Mutex
	Calls  []string // "product Action region"
	nonces map[string]bool
	nextID int

	ECSRegions, SWASRegions []aliyun.Region
	ECS                     map[string]*ECSInstance  // by ID
	SWAS                    map[string]*SWASInstance // by ID
	Groups                  map[string][]GroupRule   // ECS security group rules by group ID
	SWASRules               map[string][]SWASRule    // firewall rules by SWAS instance ID
	Snapshots               []*Snap
	Resets                  []string       // "instance snapshot" for each disk rolled back
	AlertLogs               []aliyun.Alarm // CloudMonitor's alert log
	// Metrics are monitoring samples by "instanceID metric", with the
	// metric named as the product names it.
	Metrics map[string][]Sample

	Domains []*DNSDomain
	Records map[string][]*DNSRecord // by domain name

	CDN   []*CDNDomain
	Tasks []*CDNTask

	// The account: its balance in yuan and the domains registered with it.
	Balance    float64
	Registered []aliyun.RegisteredDomain
}

// ECSInstance is an ECS instance held by the fake.
type ECSInstance struct {
	ID, Name, Region, Zone, Type, Status string
	CPU, MemoryMiB, DiskGB, Bandwidth    int
	PublicIP, PrivateIP, Group, DiskID   string
	OS, ChargeType, ExpiredTime          string // ExpiredTime as ECS writes it: 2026-12-10T04:04Z
	AutoRenew                            bool
	pending                              string // status reached on the next look
	resetting                            bool   // the system disk is being rolled back
}

// SWASInstance is a Simple Application Server held by the fake.
type SWASInstance struct {
	ID, Name, Region, Plan, Status       string
	CPU, DiskGB, Bandwidth               int
	MemoryGB                             float64
	PublicIP, PrivateIP, DiskID          string
	ImageName, ImageVersion, ExpiredTime string
	TrafficUsed, TrafficTotal            int64
	pending                              string
	resetting                            bool
}

// GroupRule is an ECS security group rule.
type GroupRule struct {
	ID, Protocol, PortRange, Source, Policy, Description string
	Priority                                             int
}

// SWASRule is a Simple Application Server firewall rule.
type SWASRule struct {
	ID, Protocol, Port, Source, Policy, Remark string
}

// Snap is a disk snapshot; Kind is "ecs" or "swas".
type Snap struct {
	ID, Name, DiskID, Instance, Region, Kind string
	Created                                  time.Time
	SizeGB                                   int
	looked                                   bool // done once it has been seen in progress
}

// Sample is one monitoring value.
type Sample struct {
	T time.Time
	V float64
}

// DNSDomain is an Alidns domain.
type DNSDomain struct {
	ID, Name, Edition, EditionCode string
	MinTTL                         int
}

// DNSRecord is an Alidns record; Status is ENABLE or DISABLE.
type DNSRecord struct {
	ID, RR, Type, Value, Line, Status, Remark string
	TTL, Priority                             int
}

// CDNDomain is an accelerated domain.
type CDNDomain struct {
	Name, Cname, Status, Origin string
}

// CDNTask is one URL or folder of a purge or prefetch task.
type CDNTask struct {
	ID, Path, Type, Status string
	Created                time.Time
	looked                 bool
}

// Regions the fake sells in.
var (
	hangzhou  = aliyun.Region{ID: "cn-hangzhou", Name: "华东1（杭州）", Endpoint: "ecs-cn-hangzhou.aliyuncs.com"}
	beijing   = aliyun.Region{ID: "cn-beijing", Name: "华北2（北京）", Endpoint: "ecs.cn-beijing.aliyuncs.com"}
	singapore = aliyun.Region{ID: "ap-southeast-1", Name: "新加坡", Endpoint: "ecs.ap-southeast-1.aliyuncs.com"}
)

// NewCloud returns a fake with, in cn-hangzhou, an ECS instance "shop"
// (subscription, auto-renewing, security group sg-shop opening 22) and a
// Simple Application Server "blog" (firewall opening 22 and 80, a 1 TB
// traffic package), a pay-as-you-go ECS instance "db" in cn-beijing, the
// Alidns domain example.com with two records, and the CDN domain
// cdn.example.com; serve it with httptest and set URL.
func NewCloud() *Cloud {
	exp := time.Now().Add(30 * 24 * time.Hour).UTC()
	f := &Cloud{
		Now:         time.Now,
		nonces:      map[string]bool{},
		nextID:      1000,
		ECSRegions:  []aliyun.Region{hangzhou, beijing, singapore},
		SWASRegions: []aliyun.Region{{ID: "cn-hangzhou", Name: "华东1（杭州）", Endpoint: "swas.cn-hangzhou.aliyuncs.com"}, {ID: "cn-shanghai", Name: "华东2（上海）", Endpoint: "swas.cn-shanghai.aliyuncs.com"}},
		ECS: map[string]*ECSInstance{
			"i-bp1shop000000000001": {ID: "i-bp1shop000000000001", Name: "shop", Region: "cn-hangzhou", Zone: "cn-hangzhou-i", Type: "ecs.e-c1m2.large",
				Status: "Running", CPU: 2, MemoryMiB: 4096, DiskGB: 40, Bandwidth: 5, PublicIP: "47.96.1.2", PrivateIP: "172.16.0.10",
				Group: "sg-shop", DiskID: "d-bp1shopsys", OS: "Alibaba Cloud Linux 3.2104 LTS 64位", ChargeType: "PrePaid",
				ExpiredTime: exp.Format("2006-01-02T15:04Z"), AutoRenew: true},
			"i-2zedb000000000000002": {ID: "i-2zedb000000000000002", Name: "db", Region: "cn-beijing", Zone: "cn-beijing-h", Type: "ecs.g7.xlarge",
				Status: "Stopped", CPU: 4, MemoryMiB: 16384, DiskGB: 100, Bandwidth: 0, PrivateIP: "172.17.0.5",
				Group: "sg-db", DiskID: "d-2zedbsys", OS: "Ubuntu 24.04 64位", ChargeType: "PostPaid", ExpiredTime: "2099-12-31T15:59Z"},
		},
		SWAS: map[string]*SWASInstance{
			"2ad1ae67295445f598017499dc000001": {ID: "2ad1ae67295445f598017499dc000001", Name: "blog", Region: "cn-hangzhou",
				Plan: "swas.s.c2m2s50b4t1", Status: "Running", CPU: 2, DiskGB: 50, Bandwidth: 4, MemoryGB: 2,
				PublicIP: "121.40.1.2", PrivateIP: "172.24.0.2", DiskID: "d-swasblogsys", ImageName: "Ubuntu", ImageVersion: "22.04",
				ExpiredTime: exp.Format("2006-01-02T15:04:05Z"), TrafficUsed: 300 << 30, TrafficTotal: 1024 << 30},
		},
		Groups: map[string][]GroupRule{
			"sg-shop": {{ID: "sgr-shop22", Protocol: "TCP", PortRange: "22/22", Source: "0.0.0.0/0", Policy: "Accept", Priority: 1}},
			"sg-db":   {},
		},
		SWASRules: map[string][]SWASRule{
			"2ad1ae67295445f598017499dc000001": {
				{ID: "fw22", Protocol: "TCP", Port: "22", Source: "0.0.0.0/0", Policy: "accept", Remark: "SSH"},
				{ID: "fw80", Protocol: "TCP", Port: "80", Source: "0.0.0.0/0", Policy: "accept", Remark: "HTTP"},
			},
		},
		Metrics: map[string][]Sample{},
		Domains: []*DNSDomain{{ID: "00efd71a-7b3c-4d2e-9f10-000000000001", Name: "example.com", Edition: "免费版", EditionCode: "mianfei", MinTTL: 600}},
		Records: map[string][]*DNSRecord{"example.com": {
			{ID: "9000000001", RR: "www", Type: "A", Value: "47.96.1.2", Line: "default", Status: "ENABLE", TTL: 600},
			{ID: "9000000002", RR: "@", Type: "MX", Value: "mx.example.net", Line: "default", Status: "ENABLE", TTL: 600, Priority: 5},
		}},
		CDN: []*CDNDomain{{Name: "cdn.example.com", Cname: "cdn.example.com.w.kunlunsl.com", Status: "online", Origin: "47.96.1.2"}},
	}
	return f
}

// StartCloud runs a fake for a test.
func StartCloud(t *testing.T) *Cloud {
	f := NewCloud()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// Endpoint routes a client to the fake, keeping the product and region
// in the path so the fake can check the client picked the right ones.
func (f *Cloud) Endpoint(product, region string) string {
	return f.URL + "/" + product + "/" + region
}

// Client returns a client that talks to the fake.
func (f *Cloud) Client() *aliyun.Client {
	c := aliyun.New(ID, Secret)
	c.EndpointFor = f.Endpoint
	return c
}

// versions says which API version each product speaks.
var versions = map[string]string{
	aliyun.ProductECS: aliyun.VersionECS, aliyun.ProductSWAS: aliyun.VersionSWAS, aliyun.ProductCMS: aliyun.VersionCMS,
	aliyun.ProductDNS: aliyun.VersionDNS, aliyun.ProductCDN: aliyun.VersionCDN,
	aliyun.ProductBSS: aliyun.VersionBSS, aliyun.ProductDomain: aliyun.VersionDomain,
}

// global products take no region.
var global = map[string]bool{aliyun.ProductDNS: true, aliyun.ProductCDN: true, aliyun.ProductBSS: true, aliyun.ProductDomain: true}

// specs are the parameters of each action the fake serves, as in the
// official API metadata; "!" marks required ones and ".N." stands for
// the index of a list. RegionId is allowed on every regional call: the
// official SDKs send it everywhere.
var specs = map[string]map[string]string{
	aliyun.ProductECS: {
		"DescribeRegions":                    "AcceptLanguage InstanceChargeType ResourceType",
		"DescribeInstances":                  "RegionId! InstanceIds MaxResults NextToken PageNumber PageSize ZoneId Status InstanceName",
		"DescribeDisks":                      "RegionId! InstanceId DiskType DiskIds MaxResults NextToken PageNumber PageSize",
		"DescribeInstanceAutoRenewAttribute": "RegionId! InstanceId PageSize PageNumber RenewalStatus",
		"StartInstance":                      "InstanceId! DryRun InitLocalDisk",
		"StopInstance":                       "InstanceId! ForceStop StoppedMode DryRun ConfirmStop Hibernate",
		"RebootInstance":                     "InstanceId! ForceStop DryRun",
		"DescribeSecurityGroupAttribute":     "RegionId! SecurityGroupId! Direction NicType MaxResults NextToken Attribute",
		"AuthorizeSecurityGroup": "RegionId! SecurityGroupId! ClientToken Permissions.N.IpProtocol Permissions.N.PortRange Permissions.N.SourceCidrIp " +
			"Permissions.N.Ipv6SourceCidrIp Permissions.N.SourceGroupId Permissions.N.SourcePrefixListId Permissions.N.Policy Permissions.N.Priority " +
			"Permissions.N.Description Permissions.N.NicType",
		"RevokeSecurityGroup":              "RegionId! SecurityGroupId! ClientToken SecurityGroupRuleId.N",
		"DescribeSnapshots":                "RegionId! DiskId InstanceId MaxResults NextToken SnapshotType SourceDiskType Status",
		"CreateSnapshot":                   "DiskId! SnapshotName Description RetentionDays",
		"ModifyInstanceAutoRenewAttribute": "RegionId! InstanceId! Duration AutoRenew RenewalStatus PeriodUnit",
		"ResetDisk":                        "DiskId! SnapshotId! DryRun",
	},
	aliyun.ProductBSS: {
		"QueryAccountBalance": "",
	},
	aliyun.ProductDomain: {
		"QueryDomainList": "PageNum! PageSize! ProductDomainType OrderKeyType OrderByType StartExpirationDate EndExpirationDate " +
			"StartRegistrationDate EndRegistrationDate DomainName QueryType GroupId Lang UserClientIp",
	},
	aliyun.ProductSWAS: {
		"ListRegions":                  "AcceptLanguage",
		"ListInstances":                "RegionId! InstanceIds PageNumber PageSize Status InstanceName ChargeType PublicIpAddresses",
		"ListInstancesTrafficPackages": "RegionId! InstanceIds!",
		"StartInstance":                "RegionId! InstanceId! ClientToken",
		"StopInstance":                 "RegionId! InstanceId! ClientToken",
		"RebootInstance":               "RegionId! InstanceId! ClientToken",
		"ListFirewallRules":            "RegionId! InstanceId! PageNumber PageSize FirewallRuleId",
		"CreateFirewallRules":          "RegionId! InstanceId! FirewallRules ClientToken",
		"DeleteFirewallRules":          "RegionId! InstanceId! RuleIds ClientToken",
		"ListDisks":                    "RegionId! InstanceId DiskIds DiskType PageNumber PageSize",
		"ListSnapshots":                "RegionId! InstanceId DiskId SnapshotIds SourceDiskType PageNumber PageSize",
		"CreateSnapshot":               "RegionId! DiskId! SnapshotName! ClientToken",
		"ResetDisk":                    "RegionId! DiskId! SnapshotId! ClientToken",
		"DescribeMonitorData":          "RegionId! InstanceId! MetricName! Period! StartTime! EndTime! Length NextToken ClientToken",
	},
	aliyun.ProductCMS: {
		"DescribeMetricList": "Namespace! MetricName! Period StartTime EndTime Dimensions NextToken Length Express",
		"DescribeAlertLogList": "StartTime EndTime PageNumber PageSize SearchKey GroupId Namespace Product Level SendStatus ContactGroup RuleName " +
			"MetricName LastMin GroupBy RuleId SourceType EventType",
	},
	aliyun.ProductDNS: {
		"DescribeDomains":          "PageNumber PageSize KeyWord SearchMode Lang GroupId",
		"DescribeDomainInfo":       "DomainName! NeedDetailAttributes Lang",
		"DescribeDomainRecords":    "DomainName! PageNumber PageSize RRKeyWord TypeKeyWord ValueKeyWord KeyWord SearchMode Type Line Status Lang OrderBy Direction",
		"DescribeDomainRecordInfo": "RecordId! Lang UserClientIp",
		"AddDomainRecord":          "DomainName! RR! Type! Value! TTL Priority Line Lang UserClientIp",
		"UpdateDomainRecord":       "RecordId! RR! Type! Value! TTL Priority Line Lang UserClientIp",
		"DeleteDomainRecord":       "RecordId! Lang UserClientIp",
		"SetDomainRecordStatus":    "RecordId! Status! Lang UserClientIp",
		"UpdateDomainRecordRemark": "RecordId! Remark Lang UserClientIp",
	},
	aliyun.ProductCDN: {
		"DescribeUserDomains":  "PageSize PageNumber DomainName DomainStatus DomainSearchType CdnType CheckDomainShow Coverage",
		"RefreshObjectCaches":  "ObjectPath! ObjectType Force",
		"PushObjectCache":      "ObjectPath! Area L2Preload WithHeader",
		"DescribeRefreshTasks": "TaskId ObjectPath PageNumber PageSize ObjectType DomainName Status StartTime EndTime",
		"StartCdnDomain":       "DomainName!",
		"StopCdnDomain":        "DomainName!",
	},
}

// maxPage is each paged action's documented largest page.
var maxPage = map[string]int{
	"ecs DescribeInstances":                  100,
	"ecs DescribeDisks":                      500,
	"ecs DescribeSnapshots":                  100,
	"ecs DescribeSecurityGroupAttribute":     1000,
	"ecs DescribeInstanceAutoRenewAttribute": 100,
	"swas-open ListInstances":                100,
	"swas-open ListFirewallRules":            100,
	"swas-open ListDisks":                    100,
	"swas-open ListSnapshots":                100,
	"swas-open DescribeMonitorData":          1440,
	"cms DescribeMetricList":                 1440,
	"alidns DescribeDomains":                 100,
	"alidns DescribeDomainRecords":           500,
	"cdn DescribeUserDomains":                500,
	"cdn DescribeRefreshTasks":               100,
	"domain QueryDomainList":                 100,
}

var common = map[string]bool{
	"Action": true, "Version": true, "Format": true, "AccessKeyId": true, "SignatureMethod": true, "SignatureVersion": true,
	"SignatureNonce": true, "Timestamp": true, "Signature": true, "SignatureType": true, "RegionId": true,
}

var index = regexp.MustCompile(`\.\d+(\.|$)`)

// apiErr is a refusal in the gateway's shape.
type apiErr struct {
	status        int
	code, message string
}

func refuse(status int, code, format string, args ...any) *apiErr {
	return &apiErr{status: status, code: code, message: fmt.Sprintf(format, args...)}
}

func (f *Cloud) write(w http.ResponseWriter, status int, body map[string]any) {
	body["RequestId"] = fmt.Sprintf("FAKE-%06d", len(f.Calls))
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *Cloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.Trim(r.URL.Path, "/"), "/", 2)
	product, region := parts[0], ""
	if len(parts) > 1 {
		region = parts[1]
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		f.write(w, 405, map[string]any{"Code": "UnsupportedHTTPMethod", "Message": "This http method is not supported."})
		return
	}
	_ = r.ParseForm()
	q := map[string]string{}
	for k, v := range r.Form {
		q[k] = v[0]
	}
	var resp map[string]any
	err := f.check(r.Method, product, region, q)
	if err == nil {
		resp, err = f.serve(product, q["Action"], region, q)
	}
	if err != nil {
		f.write(w, err.status, map[string]any{"Code": err.code, "Message": err.message,
			"HostId": product + ".aliyuncs.com", "Recommend": "https://api.aliyun.com/troubleshoot?q=" + err.code})
		return
	}
	if resp == nil {
		resp = map[string]any{}
	}
	f.write(w, 200, resp)
}

// check does what the gateway does before an action runs.
func (f *Cloud) check(method, product, region string, q map[string]string) *apiErr {
	action := q["Action"]
	f.Calls = append(f.Calls, strings.TrimSpace(product+" "+action+" "+region))
	for _, k := range []string{"AccessKeyId", "SignatureMethod", "SignatureVersion", "SignatureNonce", "Timestamp", "Version", "Action"} {
		if q[k] == "" {
			return refuse(400, "Missing"+k, "%s is mandatory for this action.", k)
		}
	}
	if q["Signature"] == "" {
		return refuse(400, "IncompleteSignature", "The request signature does not conform to Aliyun standards.")
	}
	if q["Format"] != "JSON" {
		return refuse(400, "InvalidParameter", "the fake only answers Format=JSON")
	}
	if q["AccessKeyId"] != ID {
		return refuse(404, "InvalidAccessKeyId.NotFound", "Specified access key is not found.")
	}
	if q["SignatureMethod"] != "HMAC-SHA1" || q["SignatureVersion"] != "1.0" {
		return refuse(400, "InvalidSignatureMethod", "Specified signature method is not supported.")
	}
	ts, err := time.Parse("2006-01-02T15:04:05Z", q["Timestamp"])
	if err != nil {
		return refuse(400, "InvalidTimeStamp.Format", "Specified time stamp or date value is not well formatted.")
	}
	if d := f.Now().Sub(ts); d > 15*time.Minute || d < -15*time.Minute {
		return refuse(400, "InvalidTimeStamp.Expired", "Specified time stamp or date value is expired.")
	}
	signed := map[string]string{}
	for k, v := range q {
		if k != "Signature" {
			signed[k] = v
		}
	}
	if q["Signature"] != aliyun.Signature(method, signed, Secret) {
		return refuse(400, "SignatureDoesNotMatch", "Specified signature is not matched with our calculation. server string to sign is:%s", aliyun.StringToSign(method, signed))
	}
	if f.nonces[q["SignatureNonce"]] {
		return refuse(400, "SignatureNonceUsed", "Specified signature nonce was used already.")
	}
	f.nonces[q["SignatureNonce"]] = true
	want, known := versions[product]
	if !known {
		return refuse(404, "InvalidProduct", "the fake does not serve %q", product)
	}
	if q["Version"] != want {
		return refuse(400, "InvalidVersion", "Specified parameter Version is not valid.")
	}
	spec, ok := specs[product][action]
	if !ok {
		return refuse(404, "InvalidAction.NotFound", "Specified api is not found, please check your url and method.")
	}
	if f.Denied[product] {
		return refuse(403, "Forbidden.RAM", "User not authorized to operate on the specified resource, or this API doesn't support RAM.")
	}
	allowed := map[string]bool{}
	for _, p := range strings.Fields(spec) {
		name := strings.TrimSuffix(p, "!")
		allowed[name] = true
		if strings.HasSuffix(p, "!") && q[name] == "" {
			return refuse(400, "Missing"+name, "%s is mandatory for this action.", name)
		}
	}
	for k := range q {
		if !common[k] && !allowed[index.ReplaceAllString(k, ".N$1")] {
			return refuse(400, "UnknownParameter", "%s does not take %s (the real API would ignore it: check the name)", action, k)
		}
	}
	if global[product] {
		if region != "" || q["RegionId"] != "" {
			return refuse(400, "WrongEndpoint", "%s is global: it takes no region", product)
		}
	} else if region == "" {
		return refuse(400, "WrongEndpoint", "%s is regional: call a region's endpoint", product)
	} else if q["RegionId"] != "" && q["RegionId"] != region {
		return refuse(400, "WrongEndpoint", "RegionId %s was sent to the %s endpoint", q["RegionId"], region)
	}
	if f.FailRegions[product+" "+region] {
		return refuse(500, "InternalError", "The request processing has failed due to some unknown error.")
	}
	if limit := maxPage[product+" "+action]; limit > 0 {
		for _, k := range []string{"PageSize", "MaxResults", "Length"} {
			if n, err := strconv.Atoi(q[k]); q[k] != "" && (err != nil || n < 1 || n > limit) {
				return refuse(400, "InvalidParameter", "The specified parameter %s is out of range (1 to %d).", k, limit)
			}
		}
	}
	return nil
}

func (f *Cloud) serve(product, action, region string, q map[string]string) (map[string]any, *apiErr) {
	switch product {
	case aliyun.ProductECS:
		return f.serveECS(action, region, q)
	case aliyun.ProductSWAS:
		return f.serveSWAS(action, region, q)
	case aliyun.ProductCMS:
		return f.serveCMS(action, region, q)
	case aliyun.ProductDNS:
		return f.serveDNS(action, q)
	case aliyun.ProductBSS:
		amount := strconv.FormatFloat(f.Balance, 'f', 2, 64)
		return map[string]any{"Code": "Success", "Message": "Successful!", "Success": true, "Data": map[string]any{
			"AvailableAmount": amount, "AvailableCashAmount": amount, "CreditAmount": "0.00", "MybankCreditAmount": "0.00",
			"Currency": "CNY", "QuotaLimit": "0.00"}}, nil
	case aliyun.ProductDomain:
		from, to := f.pageNumber(map[string]string{"PageSize": q["PageSize"], "PageNumber": q["PageNum"]}, 100, len(f.Registered))
		list := []map[string]any{}
		for _, d := range f.Registered[from:to] {
			list = append(list, map[string]any{"DomainName": d.Name, "ExpirationDate": d.Expires + " 23:59:59", "DomainStatus": "3",
				"RegistrationDate": "2020-01-01 10:00:00", "ProductId": "2"})
		}
		return map[string]any{"TotalItemNum": len(f.Registered), "CurrentPageNum": q["PageNum"], "PageSize": q["PageSize"],
			"Data": map[string]any{"Domain": list}}, nil
	}
	return f.serveCDN(action, q)
}

func (f *Cloud) id(prefix string) string {
	f.nextID++
	return prefix + strconv.Itoa(f.nextID)
}

// pageNumber cuts page n (from 1) of size out of a list of total items.
func (f *Cloud) pageNumber(q map[string]string, def, total int) (from, to int) {
	size, _ := strconv.Atoi(q["PageSize"])
	if size == 0 {
		size = def
	}
	if f.PageSize > 0 && size > f.PageSize {
		size = f.PageSize
	}
	n, _ := strconv.Atoi(q["PageNumber"])
	if n < 1 {
		n = 1
	}
	from = min((n-1)*size, total)
	return from, min(from+size, total)
}

// pageToken cuts a page for the NextToken style: the token is the offset.
func (f *Cloud) pageToken(q map[string]string, sizeKey string, def, total int) (from, to int, next string) {
	size, _ := strconv.Atoi(q[sizeKey])
	if size == 0 {
		size = def
	}
	if f.PageSize > 0 && size > f.PageSize {
		size = f.PageSize
	}
	if t := q["NextToken"]; t != "" {
		from, _ = strconv.Atoi(strings.TrimPrefix(t, "tok-"))
	}
	from = min(from, total)
	to = min(from+size, total)
	if to < total {
		next = "tok-" + strconv.Itoa(to)
	}
	return from, to, next
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Record finds a DNS record by ID, for assertions.
func (f *Cloud) Record(domain, id string) *DNSRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.Records[domain] {
		if r.ID == id {
			c := *r
			return &c
		}
	}
	return nil
}
