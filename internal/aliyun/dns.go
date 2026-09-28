package aliyun

import (
	"context"
	"strconv"
	"strings"
)

// DefaultLine is Alidns's default resolution line.
const DefaultLine = "default"

// Line is a resolution line: the code the API takes and its name.
type Line struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Lines are the resolution lines every edition has (the paid editions add
// provinces and more carriers; DomainInfo lists a domain's own).
var Lines = []Line{
	{"default", "默认"}, {"telecom", "电信"}, {"unicom", "联通"}, {"mobile", "移动"},
	{"edu", "教育网"}, {"oversea", "境外"}, {"baidu", "百度"}, {"biying", "必应"}, {"google", "谷歌"},
}

// Domain is a domain hosted on Alidns (云解析 DNS).
type Domain struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	PunyCode    string   `json:"punyCode,omitempty"`
	RecordCount int      `json:"recordCount"`
	DNSServers  []string `json:"dnsServers"`
	Edition     string   `json:"edition"`     // e.g. 免费版
	EditionCode string   `json:"editionCode"` // e.g. mianfei
	Remark      string   `json:"remark,omitempty"`
}

// Domains lists every domain of the account, 100 a page.
func (c *Client) Domains(ctx context.Context) ([]Domain, error) {
	var list []Domain
	for page := 1; ; page++ {
		var out struct {
			Domains struct {
				Domain []struct {
					DomainID    string `json:"DomainId"`
					DomainName  string `json:"DomainName"`
					PunyCode    string `json:"PunyCode"`
					RecordCount int    `json:"RecordCount"`
					VersionName string `json:"VersionName"`
					VersionCode string `json:"VersionCode"`
					Remark      string `json:"Remark"`
					DNSServers  struct {
						DNSServer []string `json:"DnsServer"`
					} `json:"DnsServers"`
				} `json:"Domain"`
			} `json:"Domains"`
			TotalCount int `json:"TotalCount"`
		}
		err := c.Call(ctx, ProductDNS, VersionDNS, "DescribeDomains", "", map[string]string{"PageNumber": strconv.Itoa(page), "PageSize": "100"}, &out)
		if err != nil {
			return list, err
		}
		for _, d := range out.Domains.Domain {
			list = append(list, Domain{ID: d.DomainID, Name: d.DomainName, PunyCode: d.PunyCode, RecordCount: d.RecordCount,
				DNSServers: d.DNSServers.DNSServer, Edition: d.VersionName, EditionCode: d.VersionCode, Remark: d.Remark})
		}
		if len(out.Domains.Domain) == 0 || len(list) >= out.TotalCount {
			return list, nil
		}
	}
}

// DomainInfo is what a domain's edition allows.
type DomainInfo struct {
	Name        string `json:"name"`
	Edition     string `json:"edition"`
	EditionCode string `json:"editionCode"`
	// MinTTL is the shortest TTL the edition takes, in seconds; the API
	// refuses anything shorter.
	MinTTL     int      `json:"minTTL"`
	Lines      []Line   `json:"lines"`
	DNSServers []string `json:"dnsServers"`
}

// DomainInfo reads a domain's edition, minimum TTL and resolution lines.
func (c *Client) DomainInfo(ctx context.Context, domain string) (DomainInfo, error) {
	var out struct {
		DomainName  string `json:"DomainName"`
		VersionName string `json:"VersionName"`
		VersionCode string `json:"VersionCode"`
		MinTTL      int    `json:"MinTtl"`
		RecordLines struct {
			RecordLine []struct {
				LineCode        string `json:"LineCode"`
				LineName        string `json:"LineName"`
				LineDisplayName string `json:"LineDisplayName"`
			} `json:"RecordLine"`
		} `json:"RecordLines"`
		DNSServers struct {
			DNSServer []string `json:"DnsServer"`
		} `json:"DnsServers"`
	}
	err := c.Call(ctx, ProductDNS, VersionDNS, "DescribeDomainInfo", "", map[string]string{"DomainName": domain, "NeedDetailAttributes": "true"}, &out)
	info := DomainInfo{Name: out.DomainName, Edition: out.VersionName, EditionCode: out.VersionCode, MinTTL: out.MinTTL, DNSServers: out.DNSServers.DNSServer}
	for _, l := range out.RecordLines.RecordLine {
		name := l.LineDisplayName
		if name == "" {
			name = l.LineName
		}
		info.Lines = append(info.Lines, Line{Code: l.LineCode, Name: name})
	}
	return info, err
}

// Record is one DNS record.
type Record struct {
	ID       string `json:"id"`
	Name     string `json:"name"` // host record (RR), "@" for the domain itself
	Type     string `json:"type"`
	Value    string `json:"value"`
	Line     string `json:"line"` // a line code, "default" when empty
	TTL      int    `json:"ttl"`  // seconds; 600 when zero
	Priority int    `json:"priority,omitempty"`
	// Status is "enabled" or "disabled" (paused); empty leaves it alone.
	Status string `json:"status"`
	Remark string `json:"remark,omitempty"`
	Locked bool   `json:"locked,omitempty"`
	Weight int    `json:"weight,omitempty"`
}

func recordStatus(s string) string {
	if strings.EqualFold(s, "disable") || strings.EqualFold(s, "disabled") {
		return "disabled"
	}
	return "enabled"
}

// Records lists every record of a domain, 500 a page.
func (c *Client) Records(ctx context.Context, domain string) ([]Record, error) {
	var list []Record
	for page := 1; ; page++ {
		var out struct {
			DomainRecords struct {
				Record []struct {
					RecordID string `json:"RecordId"`
					RR       string `json:"RR"`
					Type     string `json:"Type"`
					Value    string `json:"Value"`
					Line     string `json:"Line"`
					TTL      int    `json:"TTL"`
					Priority int    `json:"Priority"`
					Status   string `json:"Status"`
					Remark   string `json:"Remark"`
					Locked   bool   `json:"Locked"`
					Weight   int    `json:"Weight"`
				} `json:"Record"`
			} `json:"DomainRecords"`
			TotalCount int `json:"TotalCount"`
		}
		err := c.Call(ctx, ProductDNS, VersionDNS, "DescribeDomainRecords", "",
			map[string]string{"DomainName": domain, "PageNumber": strconv.Itoa(page), "PageSize": "500"}, &out)
		if err != nil {
			return list, err
		}
		for _, r := range out.DomainRecords.Record {
			rec := Record{ID: r.RecordID, Name: r.RR, Type: r.Type, Value: r.Value, Line: r.Line, TTL: r.TTL,
				Status: recordStatus(r.Status), Remark: r.Remark, Locked: r.Locked, Weight: r.Weight}
			if r.Type == "MX" {
				rec.Priority = r.Priority
			}
			list = append(list, rec)
		}
		if len(out.DomainRecords.Record) == 0 || len(list) >= out.TotalCount {
			return list, nil
		}
	}
}

// Record reads one record by its ID.
func (c *Client) Record(ctx context.Context, id string) (Record, error) {
	var out struct {
		RecordID string `json:"RecordId"`
		RR       string `json:"RR"`
		Type     string `json:"Type"`
		Value    string `json:"Value"`
		Line     string `json:"Line"`
		TTL      int    `json:"TTL"`
		Priority int    `json:"Priority"`
		Status   string `json:"Status"`
		Remark   string `json:"Remark"`
		Locked   bool   `json:"Locked"`
	}
	err := c.Call(ctx, ProductDNS, VersionDNS, "DescribeDomainRecordInfo", "", map[string]string{"RecordId": id}, &out)
	rec := Record{ID: out.RecordID, Name: out.RR, Type: out.Type, Value: out.Value, Line: out.Line, TTL: out.TTL,
		Status: recordStatus(out.Status), Remark: out.Remark, Locked: out.Locked}
	if out.Type == "MX" {
		rec.Priority = out.Priority
	}
	return rec, err
}

// recordFields are what AddDomainRecord and UpdateDomainRecord share.
func recordFields(r Record) map[string]string {
	in := map[string]string{"RR": r.Name, "Type": strings.ToUpper(r.Type), "Value": r.Value}
	if in["RR"] == "" {
		in["RR"] = "@"
	}
	if r.Line != "" {
		in["Line"] = r.Line
	}
	if r.TTL > 0 {
		in["TTL"] = strconv.Itoa(r.TTL)
	}
	if in["Type"] == "MX" {
		p := r.Priority
		if p == 0 {
			p = 10
		}
		in["Priority"] = strconv.Itoa(p)
	}
	return in
}

// AddRecord adds a record to a domain and returns its ID. A remark or a
// disabled status take a call each after it, as the API wants.
func (c *Client) AddRecord(ctx context.Context, domain string, r Record) (string, error) {
	in := recordFields(r)
	in["DomainName"] = domain
	var out struct {
		RecordID string `json:"RecordId"`
	}
	if err := c.Call(ctx, ProductDNS, VersionDNS, "AddDomainRecord", "", in, &out); err != nil {
		return "", err
	}
	if r.Remark != "" {
		if err := c.SetRecordRemark(ctx, out.RecordID, r.Remark); err != nil {
			return out.RecordID, err
		}
	}
	if r.Status == "disabled" {
		return out.RecordID, c.SetRecordStatus(ctx, out.RecordID, false)
	}
	return out.RecordID, nil
}

// UpdateRecord makes a record match r (by r.ID): name, type, value, line,
// TTL and priority, then remark and status when they differ. Alidns
// refuses an update that changes nothing, so an unchanged record is not
// sent.
func (c *Client) UpdateRecord(ctx context.Context, r Record) error {
	cur, err := c.Record(ctx, r.ID)
	if err != nil {
		return err
	}
	want, have := recordFields(r), recordFields(cur)
	for _, k := range []string{"Line", "TTL"} { // left out: keep what it has
		if _, set := want[k]; !set && have[k] != "" {
			want[k] = have[k]
		}
	}
	changed := len(want) != len(have)
	for k, v := range want {
		changed = changed || have[k] != v
	}
	if changed {
		want["RecordId"] = r.ID
		if err := c.Call(ctx, ProductDNS, VersionDNS, "UpdateDomainRecord", "", want, nil); err != nil {
			return err
		}
	}
	if r.Remark != cur.Remark {
		if err := c.SetRecordRemark(ctx, r.ID, r.Remark); err != nil {
			return err
		}
	}
	if r.Status != "" && r.Status != cur.Status {
		return c.SetRecordStatus(ctx, r.ID, r.Status == "enabled")
	}
	return nil
}

// DeleteRecord removes a record.
func (c *Client) DeleteRecord(ctx context.Context, id string) error {
	return c.Call(ctx, ProductDNS, VersionDNS, "DeleteDomainRecord", "", map[string]string{"RecordId": id}, nil)
}

// SetRecordStatus resumes (enabled) or pauses a record.
func (c *Client) SetRecordStatus(ctx context.Context, id string, enabled bool) error {
	status := "Disable"
	if enabled {
		status = "Enable"
	}
	return c.Call(ctx, ProductDNS, VersionDNS, "SetDomainRecordStatus", "", map[string]string{"RecordId": id, "Status": status}, nil)
}

// SetRecordRemark replaces a record's remark; empty removes it.
func (c *Client) SetRecordRemark(ctx context.Context, id, remark string) error {
	return c.Call(ctx, ProductDNS, VersionDNS, "UpdateDomainRecordRemark", "", map[string]string{"RecordId": id, "Remark": remark}, nil)
}
