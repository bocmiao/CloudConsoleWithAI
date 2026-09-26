package tencent

import "context"

const dnspodVersion = "2021-03-23"

// DefaultLine is DNSPod's default resolution line.
const DefaultLine = "默认"

// Domain is a domain hosted on DNSPod.
type Domain struct {
	DomainID     uint64   `json:"DomainId"`
	Name         string   `json:"Name"`
	Status       string   `json:"Status"`    // ENABLE, PAUSE, SPAM
	DNSStatus    string   `json:"DNSStatus"` // DNSERROR when the name servers are not DNSPod's
	Grade        string   `json:"Grade"`
	RecordCount  uint64   `json:"RecordCount"`
	EffectiveDNS []string `json:"EffectiveDNS"`
}

// Domains lists the account's DNSPod domains.
func (c *Client) Domains(ctx context.Context) ([]Domain, error) {
	var out struct {
		DomainList []Domain `json:"DomainList"`
	}
	err := c.Call(ctx, "dnspod", dnspodVersion, "DescribeDomainList", map[string]any{"Type": "ALL", "Limit": 3000}, &out)
	return out.DomainList, err
}

// Record is one DNS record.
type Record struct {
	RecordID uint64 `json:"RecordId"`
	Name     string `json:"Name"` // subdomain, "@" for the domain itself
	Type     string `json:"Type"`
	Value    string `json:"Value"`
	Line     string `json:"Line"`
	TTL      uint64 `json:"TTL"`
	MX       uint64 `json:"MX"`
	Status   string `json:"Status"` // ENABLE, DISABLE
	Remark   string `json:"Remark"`
	// Weight is set when records of a name share traffic by weight.
	Weight    *uint64 `json:"Weight,omitempty"`
	UpdatedOn string  `json:"UpdatedOn,omitempty"`
	// DefaultNS marks the NS records DNSPod keeps for the domain itself.
	DefaultNS bool `json:"DefaultNS,omitempty"`
}

// Records lists a domain's records, only those of subdomain when given.
func (c *Client) Records(ctx context.Context, domain, subdomain string) ([]Record, error) {
	in := map[string]any{"Domain": domain, "Limit": 3000, "ErrorOnEmpty": "no"}
	if subdomain != "" {
		in["Subdomain"] = subdomain
	}
	var out struct {
		RecordList []Record `json:"RecordList"`
	}
	err := c.Call(ctx, "dnspod", dnspodVersion, "DescribeRecordList", in, &out)
	if IsCode(err, "ResourceNotFound.NoDataOfRecord") {
		return nil, nil
	}
	return out.RecordList, err
}

func recordFields(domain string, r Record) map[string]any {
	in := map[string]any{"Domain": domain, "SubDomain": r.Name, "RecordType": r.Type, "RecordLine": r.Line, "Value": r.Value}
	if r.TTL > 0 {
		in["TTL"] = r.TTL
	}
	if r.Type == "MX" {
		in["MX"] = r.MX
	}
	if r.Remark != "" {
		in["Remark"] = r.Remark
	}
	// Left out, a change would switch a paused record back on and drop its
	// weight.
	if r.Status != "" {
		in["Status"] = r.Status
	}
	if r.Weight != nil {
		in["Weight"] = *r.Weight
	}
	return in
}

// CreateRecord adds a record and returns its ID.
func (c *Client) CreateRecord(ctx context.Context, domain string, r Record) (uint64, error) {
	var out struct {
		RecordID uint64 `json:"RecordId"`
	}
	err := c.Call(ctx, "dnspod", dnspodVersion, "CreateRecord", recordFields(domain, r), &out)
	return out.RecordID, err
}

// ModifyRecord replaces a record's type, value, line and TTL.
func (c *Client) ModifyRecord(ctx context.Context, domain string, r Record) error {
	in := recordFields(domain, r)
	in["RecordId"] = r.RecordID
	return c.Call(ctx, "dnspod", dnspodVersion, "ModifyRecord", in, nil)
}

// DeleteRecord removes a record.
func (c *Client) DeleteRecord(ctx context.Context, domain string, id uint64) error {
	return c.Call(ctx, "dnspod", dnspodVersion, "DeleteRecord", map[string]any{"Domain": domain, "RecordId": id}, nil)
}

// SetRecordStatus pauses (DISABLE) or resumes (ENABLE) a record.
func (c *Client) SetRecordStatus(ctx context.Context, domain string, id uint64, status string) error {
	return c.Call(ctx, "dnspod", dnspodVersion, "ModifyRecordStatus", map[string]any{"Domain": domain, "RecordId": id, "Status": status}, nil)
}

// RecordLines lists the resolution lines the domain's plan offers
// (默认, 电信, 联通, 境外……), by name.
func (c *Client) RecordLines(ctx context.Context, domain, grade string) ([]string, error) {
	var out struct {
		LineList []struct {
			Name string `json:"Name"`
		} `json:"LineList"`
	}
	if err := c.Call(ctx, "dnspod", dnspodVersion, "DescribeRecordLineList", map[string]any{"Domain": domain, "DomainGrade": grade}, &out); err != nil {
		return nil, err
	}
	var names []string
	for _, l := range out.LineList {
		names = append(names, l.Name)
	}
	return names, nil
}
