// Package tencenttest is a fake of the Tencent Cloud APIs Miao Panel uses
// (DNSPod and EdgeOne), for tests. It checks request signatures and keeps
// state like the real services do.
package tencenttest

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// Credentials the fake accepts.
const (
	SecretID  = "AKIDfaketestsecretid0001"
	SecretKey = "fake-secret-key-0001"
)

// Record is a DNSPod record held by the fake.
type Record = tencent.Record

// Domain is an EdgeOne acceleration domain held by the fake.
type Domain struct {
	Zone, Name, Status, Cname, Origin, Protocol string
	CertMode, CertStatus                        string
}

// Fake is the running fake.
type Fake struct {
	URL string

	mu      sync.Mutex
	nextID  uint64
	Records map[string][]Record // by DNSPod domain
	Zones   []tencent.Zone
	Domains []*Domain
	Calls   []string // "service Action"
	// DeniedService makes every call to that service fail with a
	// permission error.
	DeniedService string
	// FailAction makes calls to one "service Action" fail.
	FailAction string

	// Servers: a Lighthouse instance and a CVM instance in ap-guangzhou.
	Instances map[string]*Instance
	Firewall  map[string][]tencent.FirewallRule // by instance or security group
	Snaps     map[string]*tencent.Snapshot
	Regions   []string // regions the fake saw requests for

	// EdgeOne: site-level security policies by zone, and plans.
	Policies map[string]map[string]any
	Plans    []tencent.Plan
	// ClientIPHeaders are the sites' "client IP header" settings by zone.
	ClientIPHeaders map[string]map[string]any
	// EdgeOneNodes are the IPs DescribeIPRegion calls EdgeOne's own.
	EdgeOneNodes map[string]bool
	// L7Logs are offline log packages by zone: each a name and its JSON
	// lines, served gzipped under /eolog/.
	L7Logs map[string][]LogPackage

	// TAT: which instances have the agent online, and a function that
	// plays the server running a command (defaults to echoing nothing).
	AgentOnline map[string]bool
	RunShell    func(instance, script string) (output string, exitCode int)
	invocations map[string]*invocation
}

// Instance is a server held by the fake.
type Instance struct {
	ID, Name, State, IP, Group, DiskID string
	pending                            string // state reached on the next look
}

// New returns a fake with one DNSPod domain and one EdgeOne site (CNAME
// access) for example.com, a Lighthouse and a CVM instance; serve it with
// httptest and set URL.
func New() *Fake {
	f := &Fake{
		Instances: map[string]*Instance{
			"lhins-abc12345": {ID: "lhins-abc12345", Name: "blog", State: "RUNNING", IP: "81.68.79.253", DiskID: "lhdisk-1"},
			"ins-xyz98765":   {ID: "ins-xyz98765", Name: "shop", State: "RUNNING", IP: "43.1.2.3", Group: "sg-abc", DiskID: "disk-9"},
		},
		Firewall: map[string][]tencent.FirewallRule{
			"lhins-abc12345": {{Protocol: "TCP", Port: "22", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"}, {Protocol: "TCP", Port: "80", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"}},
			"sg-abc":         {{Protocol: "TCP", Port: "22", CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Index: 0}},
		},
		Snaps:   map[string]*tencent.Snapshot{},
		nextID:  100,
		Records: map[string][]Record{"example.com": {{RecordID: 1, Name: "blog", Type: "A", Value: "1.2.3.4", Line: tencent.DefaultLine, TTL: 600, Status: "ENABLE"}}},
		Zones:   []tencent.Zone{{ZoneID: "zone-abc", ZoneName: "example.com", Type: "partial", Status: "active", Area: "mainland", CnameStatus: "finished"}},
		Policies: map[string]map[string]any{"zone-abc": {
			"CustomRules": map[string]any{"Rules": []any{map[string]any{"Id": "rule-1", "Name": "屏蔽海外", "Condition": "${http.request.ip.country} in ['US']",
				"Action": map[string]any{"Name": "Deny"}, "Enabled": "on", "RuleType": "BasicAccessRule"}}},
			"RateLimitingRules":  map[string]any{"Rules": []any{}},
			"HttpDDoSProtection": map[string]any{"AdaptiveFrequencyControl": map[string]any{"Id": "afc-1", "Enabled": "off"}, "ClientFiltering": map[string]any{"Id": "cf-1", "Enabled": "on", "Action": map[string]any{"Name": "Monitor"}}},
		}},
		Plans: []tencent.Plan{
			{PlanID: "edgeone-full1", PlanType: "plan-personal", Area: "mainland", Status: "normal", Bindable: "false"},
			{PlanID: "edgeone-free2", PlanType: "plan-basic", Area: "global", Status: "normal", Bindable: "true"},
		},
		AgentOnline: map[string]bool{"lhins-abc12345": true},
		invocations: map[string]*invocation{},
	}
	return f
}

// Start runs a fake for a test.
func Start(t *testing.T) *Fake {
	f := New()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// Endpoint routes a client to the fake.
func (f *Fake) Endpoint(service string) string { return f.URL + "/" + service }

// Client returns a client that talks to the fake.
func (f *Fake) Client() *tencent.Client {
	c := tencent.New(SecretID, SecretKey)
	c.Endpoint = f.Endpoint
	return c
}

var credRe = regexp.MustCompile(`Credential=([^/]+)/(\d{4}-\d{2}-\d{2})/([a-z]+)/tc3_request`)

func fail(w http.ResponseWriter, code, msg string) {
	data, _ := json.Marshal(map[string]any{"Response": map[string]any{"Error": map[string]string{"Code": code, "Message": msg}, "RequestId": "req-err"}})
	_, _ = w.Write(data)
}

func ok(w http.ResponseWriter, resp map[string]any) {
	if resp == nil {
		resp = map[string]any{}
	}
	resp["RequestId"] = "req-ok"
	data, _ := json.Marshal(map[string]any{"Response": resp})
	_, _ = w.Write(data)
}

// LogPackage is an EdgeOne offline log package.
type LogPackage struct {
	Domain, Name, Lines string
	Start               time.Time
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if name, ok := strings.CutPrefix(r.URL.Path, "/eolog/"); ok {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, list := range f.L7Logs {
			for _, p := range list {
				if p.Name == name {
					f.Calls = append(f.Calls, "GET "+name)
					gz := gzip.NewWriter(w)
					_, _ = io.WriteString(gz, p.Lines)
					_ = gz.Close()
					return
				}
			}
		}
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	service := strings.Trim(r.URL.Path, "/")
	action := r.Header.Get("X-TC-Action")
	ts, _ := strconv.ParseInt(r.Header.Get("X-TC-Timestamp"), 10, 64)
	m := credRe.FindStringSubmatch(r.Header.Get("Authorization"))
	if m == nil || m[1] != SecretID || m[3] != service ||
		r.Header.Get("Authorization") != tencent.Authorization(SecretID, SecretKey, service, r.Host, ts, body) {
		fail(w, "AuthFailure.SignatureFailure", "The provided credentials could not be validated.")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, service+" "+action)
	if service+" "+action == f.FailAction {
		fail(w, "InternalError", "the fake was told to fail "+f.FailAction)
		return
	}
	if service == f.DeniedService {
		fail(w, "UnauthorizedOperation", "you are not authorized to perform operation ("+service+":"+action+")")
		return
	}
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	num := func(k string) uint64 { n, _ := in[k].(float64); return uint64(n) }
	strs := func(k string) []string {
		var out []string
		list, _ := in[k].([]any)
		for _, v := range list {
			s, _ := v.(string)
			out = append(out, s)
		}
		return out
	}
	switch service + " " + action {
	case "dnspod DescribeDomainList":
		var list []map[string]any
		for name, recs := range f.Records {
			list = append(list, map[string]any{"Name": name, "Status": "ENABLE", "Grade": "DP_FREE", "RecordCount": len(recs)})
		}
		ok(w, map[string]any{"DomainList": list})
	case "dnspod DescribeRecordList":
		recs, found := f.Records[str("Domain")]
		if !found {
			fail(w, "InvalidParameterValue.DomainNotExists", "当前域名有误，请返回重新操作。")
			return
		}
		var out []Record
		for _, rec := range recs {
			if sub := str("Subdomain"); sub == "" || rec.Name == sub {
				out = append(out, rec)
			}
		}
		ok(w, map[string]any{"RecordList": out})
	case "dnspod CreateRecord":
		rec := f.recordFrom(in, str, num)
		if f.conflicts(str("Domain"), rec, 0) {
			fail(w, "InvalidParameter.RecordConflict", "记录有冲突。")
			return
		}
		f.nextID++
		rec.RecordID = f.nextID
		f.Records[str("Domain")] = append(f.Records[str("Domain")], rec)
		ok(w, map[string]any{"RecordId": rec.RecordID})
	case "dnspod ModifyRecord":
		recs := f.Records[str("Domain")]
		for i := range recs {
			if recs[i].RecordID == num("RecordId") {
				next := f.recordFrom(in, str, num)
				next.RecordID = recs[i].RecordID
				if f.conflicts(str("Domain"), next, next.RecordID) {
					fail(w, "InvalidParameter.RecordConflict", "记录有冲突。")
					return
				}
				recs[i] = next
				ok(w, nil)
				return
			}
		}
		fail(w, "InvalidParameter.RecordIdInvalid", "记录编号错误。")
	case "dnspod ModifyRecordStatus":
		recs := f.Records[str("Domain")]
		for i := range recs {
			if recs[i].RecordID == num("RecordId") {
				recs[i].Status = str("Status")
				ok(w, nil)
				return
			}
		}
		fail(w, "InvalidParameter.RecordIdInvalid", "记录编号错误。")
	case "dnspod DescribeRecordLineList":
		if str("DomainGrade") == "" {
			fail(w, "MissingParameter", "缺少参数 DomainGrade。")
			return
		}
		var lines []map[string]string
		for i, n := range []string{tencent.DefaultLine, "电信", "联通", "移动", "教育网", "境外", "搜索引擎"} {
			lines = append(lines, map[string]string{"Name": n, "LineId": strconv.Itoa(i)})
		}
		ok(w, map[string]any{"LineList": lines})
	case "dnspod DeleteRecord":
		recs := f.Records[str("Domain")]
		for i := range recs {
			if recs[i].RecordID == num("RecordId") {
				f.Records[str("Domain")] = append(recs[:i:i], recs[i+1:]...)
				ok(w, nil)
				return
			}
		}
		fail(w, "InvalidParameter.RecordIdInvalid", "记录编号错误。")
	case "teo DescribeZones":
		ok(w, map[string]any{"TotalCount": len(f.Zones), "Zones": f.Zones})
	case "teo DescribeAccelerationDomains":
		want := ""
		if fl, _ := in["Filters"].([]any); len(fl) > 0 {
			if v, _ := fl[0].(map[string]any)["Values"].([]any); len(v) > 0 {
				want, _ = v[0].(string)
			}
		}
		var out []map[string]any
		for _, d := range f.Domains {
			if d.Zone != str("ZoneId") || (want != "" && d.Name != want) {
				continue
			}
			var certs []map[string]string
			if d.CertMode == "eofreecert" {
				certs = []map[string]string{{"CertId": "eofree-1", "Type": "managed", "Status": d.CertStatus,
					"ExpireTime": time.Now().Add(80 * 24 * time.Hour).UTC().Format(time.RFC3339)}}
				if d.CertStatus == "applying" {
					d.CertStatus = "deployed" // issued by the next look
				}
			}
			out = append(out, map[string]any{
				"ZoneId": d.Zone, "DomainName": d.Name, "DomainStatus": d.Status, "Cname": d.Cname,
				"OriginDetail": map[string]string{"OriginType": "IP_DOMAIN", "Origin": d.Origin}, "OriginProtocol": d.Protocol,
				"HttpOriginPort": 80, "HttpsOriginPort": 443,
				"Certificate": map[string]any{"Mode": d.CertMode, "List": certs},
			})
		}
		ok(w, map[string]any{"TotalCount": len(out), "AccelerationDomains": out})
	case "teo CreateAccelerationDomain":
		for _, d := range f.Domains {
			if d.Name == str("DomainName") {
				fail(w, "ResourceInUse.Others", "域名已存在。")
				return
			}
		}
		origin, _ := in["OriginInfo"].(map[string]any)
		o, _ := origin["Origin"].(string)
		f.Domains = append(f.Domains, &Domain{Zone: str("ZoneId"), Name: str("DomainName"), Status: "online",
			Cname: str("DomainName") + ".eo.dnse4.com", Origin: o, Protocol: str("OriginProtocol"), CertMode: "disable"})
		ok(w, nil)
	case "teo ModifyAccelerationDomainStatuses":
		for _, name := range strs("DomainNames") {
			for _, d := range f.Domains {
				if d.Name == name {
					d.Status = str("Status")
				}
			}
		}
		ok(w, nil)
	case "teo DeleteAccelerationDomains":
		for _, name := range strs("DomainNames") {
			for i, d := range f.Domains {
				if d.Name == name {
					if d.Status != "offline" {
						fail(w, "OperationDenied.DomainStatusNotOffline", "请先停用域名。")
						return
					}
					f.Domains = append(f.Domains[:i:i], f.Domains[i+1:]...)
					break
				}
			}
		}
		ok(w, nil)
	case "teo CheckCnameStatus":
		var out []map[string]string
		for _, name := range strs("RecordNames") {
			status := "moved"
			for _, d := range f.Domains {
				if d.Name == name && f.resolvesTo(name, d.Cname) {
					status = "active"
				}
			}
			out = append(out, map[string]string{"RecordName": name, "Status": status})
		}
		ok(w, map[string]any{"CnameStatus": out})
	case "teo ModifyHostsCertificate":
		for _, name := range strs("Hosts") {
			for _, d := range f.Domains {
				if d.Name == name {
					d.CertMode, d.CertStatus = str("Mode"), ""
					if d.CertMode == "eofreecert" {
						d.CertStatus = "applying"
					}
				}
			}
		}
		ok(w, nil)
	default:
		if !f.serveMore(w, service, action, r.Header.Get("X-TC-Region"), in) {
			fail(w, "InvalidAction", fmt.Sprintf("fake does not implement %s %s", service, action))
		}
	}
}

// recordFrom reads a record from a CreateRecord or ModifyRecord request,
// with DNSPod's defaults.
func (f *Fake) recordFrom(in map[string]any, str func(string) string, num func(string) uint64) Record {
	rec := Record{Name: str("SubDomain"), Type: str("RecordType"), Value: str("Value"), Line: str("RecordLine"), TTL: num("TTL"),
		MX: num("MX"), Remark: str("Remark"), Status: str("Status")}
	if rec.Status == "" {
		rec.Status = "ENABLE"
	}
	if rec.TTL == 0 {
		rec.TTL = 600
	}
	if _, set := in["Weight"]; set {
		w := num("Weight")
		rec.Weight = &w
	}
	return rec
}

// conflicts applies DNSPod's rule that a CNAME cannot share a name and
// line with A, AAAA or another CNAME record.
func (f *Fake) conflicts(domain string, rec Record, except uint64) bool {
	for _, o := range f.Records[domain] {
		if o.RecordID == except || o.Name != rec.Name || o.Line != rec.Line {
			continue
		}
		isAddr := func(t string) bool { return t == "A" || t == "AAAA" || t == "CNAME" }
		if (rec.Type == "CNAME" && isAddr(o.Type)) || (o.Type == "CNAME" && isAddr(rec.Type)) {
			return true
		}
	}
	return false
}

func (f *Fake) resolvesTo(name, cname string) bool {
	for domain, recs := range f.Records {
		for _, r := range recs {
			full := r.Name + "." + domain
			if r.Name == "@" {
				full = domain
			}
			if full == name && r.Type == "CNAME" && strings.TrimSuffix(r.Value, ".") == cname {
				return true
			}
		}
	}
	return false
}

// Lookup returns the records of a name, for assertions.
func (f *Fake) Lookup(domain, sub string) []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Record
	for _, r := range f.Records[domain] {
		if r.Name == sub {
			out = append(out, r)
		}
	}
	return out
}

// Domain returns an acceleration domain, for assertions.
func (f *Fake) Domain(name string) *Domain {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.Domains {
		if d.Name == name {
			c := *d
			return &c
		}
	}
	return nil
}
