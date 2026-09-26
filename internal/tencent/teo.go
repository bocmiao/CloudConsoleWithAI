package tencent

import (
	"context"
	"strings"
	"time"
)

const teoVersion = "2022-09-01"

// Zone is an EdgeOne site.
type Zone struct {
	ZoneID     string `json:"ZoneId"`
	ZoneName   string `json:"ZoneName"`
	Type       string `json:"Type"`   // full (NS), partial (CNAME), dnsPodAccess, noDomainAccess, ...
	Status     string `json:"Status"` // active, pending, moved, deactivated, initializing
	Area       string `json:"Area"`   // global, mainland, overseas
	Paused     bool   `json:"Paused"`
	CnameSpeed string `json:"CnameSpeedUp"`
	// CnameStatus is finished once a CNAME site proved the domain is yours.
	CnameStatus string       `json:"CnameStatus"`
	CNAMEDetail *CNAMEDetail `json:"CNAMEDetail,omitempty"`
}

// CNAMEDetail describes a CNAME site.
type CNAMEDetail struct {
	OwnershipVerification *Ownership `json:"OwnershipVerification"`
}

// Ownership is how EdgeOne asks you to prove a domain is yours.
type Ownership struct {
	DNSVerification *DNSVerification `json:"DnsVerification"`
}

// Verification is the TXT record a CNAME site still waits for, if any.
func (z Zone) Verification() *DNSVerification {
	if z.Type != "partial" || z.CnameStatus != "pending" || z.CNAMEDetail == nil || z.CNAMEDetail.OwnershipVerification == nil {
		return nil
	}
	return z.CNAMEDetail.OwnershipVerification.DNSVerification
}

// Zones lists the account's EdgeOne sites.
func (c *Client) Zones(ctx context.Context) ([]Zone, error) {
	var out struct {
		Zones []Zone `json:"Zones"`
	}
	err := c.Call(ctx, "teo", teoVersion, "DescribeZones", map[string]any{"Limit": 100}, &out)
	return out.Zones, err
}

// ZoneFor finds the site a domain belongs to: the longest zone name the
// domain equals or ends with.
func ZoneFor(zones []Zone, domain string) (Zone, bool) {
	var best Zone
	for _, z := range zones {
		if (domain == z.ZoneName || strings.HasSuffix(domain, "."+z.ZoneName)) && len(z.ZoneName) > len(best.ZoneName) {
			best = z
		}
	}
	return best, best.ZoneID != ""
}

// Certificate is one certificate deployed on an acceleration domain.
type Certificate struct {
	CertID     string `json:"CertId"`
	Type       string `json:"Type"`   // default, upload, managed
	Status     string `json:"Status"` // deployed, processing, applying, failed, issued
	ExpireTime string `json:"ExpireTime"`
}

// AccelerationDomain is a domain served through EdgeOne.
type AccelerationDomain struct {
	ZoneID       string `json:"ZoneId"`
	DomainName   string `json:"DomainName"`
	DomainStatus string `json:"DomainStatus"` // online, process, offline, init
	Cname        string `json:"Cname"`
	OriginDetail struct {
		OriginType string `json:"OriginType"`
		Origin     string `json:"Origin"`
		HostHeader string `json:"HostHeader"`
	} `json:"OriginDetail"`
	OriginProtocol  string `json:"OriginProtocol"` // FOLLOW, HTTP, HTTPS
	HTTPOriginPort  uint64 `json:"HttpOriginPort"`
	HTTPSOriginPort uint64 `json:"HttpsOriginPort"`
	Certificate     struct {
		Mode string        `json:"Mode"` // disable, eofreecert, sslcert, ...
		List []Certificate `json:"List"`
	} `json:"Certificate"`
}

// AccelerationDomains lists a site's acceleration domains, or only the
// named one.
func (c *Client) AccelerationDomains(ctx context.Context, zoneID, name string) ([]AccelerationDomain, error) {
	in := map[string]any{"ZoneId": zoneID, "Limit": 200}
	if name != "" {
		in["Filters"] = []map[string]any{{"Name": "domain-name", "Values": []string{name}}}
	}
	var out struct {
		AccelerationDomains []AccelerationDomain `json:"AccelerationDomains"`
	}
	err := c.Call(ctx, "teo", teoVersion, "DescribeAccelerationDomains", in, &out)
	return out.AccelerationDomains, err
}

// AccelerationDomain returns one acceleration domain; ok is false when the
// site has no such domain.
func (c *Client) AccelerationDomain(ctx context.Context, zoneID, name string) (AccelerationDomain, bool, error) {
	list, err := c.AccelerationDomains(ctx, zoneID, name)
	for _, d := range list {
		if strings.EqualFold(d.DomainName, name) {
			return d, true, err
		}
	}
	return AccelerationDomain{}, false, err
}

// NewDomain is what creating an acceleration domain needs.
type NewDomain struct {
	ZoneID    string
	Name      string
	Origin    string // IP or host name of the origin server
	Protocol  string // FOLLOW, HTTP, HTTPS
	HTTPPort  int
	HTTPSPort int
}

// DNSVerification is a DNS record EdgeOne asks for to prove the domain is yours.
type DNSVerification struct {
	Subdomain   string `json:"Subdomain"`
	RecordType  string `json:"RecordType"`
	RecordValue string `json:"RecordValue"`
}

// CreateAccelerationDomain adds a domain to a site. When the site's
// ownership is not verified yet, EdgeOne returns the record to add.
func (c *Client) CreateAccelerationDomain(ctx context.Context, d NewDomain) (*DNSVerification, error) {
	var out struct {
		OwnershipVerification *Ownership `json:"OwnershipVerification"`
	}
	err := c.Call(ctx, "teo", teoVersion, "CreateAccelerationDomain", map[string]any{
		"ZoneId": d.ZoneID, "DomainName": d.Name,
		"OriginInfo":     map[string]any{"OriginType": "IP_DOMAIN", "Origin": d.Origin},
		"OriginProtocol": d.Protocol, "HttpOriginPort": d.HTTPPort, "HttpsOriginPort": d.HTTPSPort,
	}, &out)
	if out.OwnershipVerification != nil && out.OwnershipVerification.DNSVerification != nil &&
		out.OwnershipVerification.DNSVerification.RecordValue != "" {
		return out.OwnershipVerification.DNSVerification, err
	}
	return nil, err
}

// SetAccelerationDomainStatus switches domains online or offline.
func (c *Client) SetAccelerationDomainStatus(ctx context.Context, zoneID string, names []string, status string) error {
	return c.Call(ctx, "teo", teoVersion, "ModifyAccelerationDomainStatuses",
		map[string]any{"ZoneId": zoneID, "DomainNames": names, "Status": status}, nil)
}

// DeleteAccelerationDomains removes domains from a site (they must be offline).
func (c *Client) DeleteAccelerationDomains(ctx context.Context, zoneID string, names []string) error {
	return c.Call(ctx, "teo", teoVersion, "DeleteAccelerationDomains",
		map[string]any{"ZoneId": zoneID, "DomainNames": names}, nil)
}

// CnameStatus checks whether a domain's DNS already points at the CNAME
// EdgeOne assigned: active, moved (not yet) or invalid (someone else's).
func (c *Client) CnameStatus(ctx context.Context, zoneID, name string) (string, error) {
	var out struct {
		CnameStatus []struct {
			RecordName string `json:"RecordName"`
			Status     string `json:"Status"`
		} `json:"CnameStatus"`
	}
	err := c.Call(ctx, "teo", teoVersion, "CheckCnameStatus", map[string]any{"ZoneId": zoneID, "RecordNames": []string{name}}, &out)
	for _, s := range out.CnameStatus {
		if strings.EqualFold(s.RecordName, name) {
			return s.Status, err
		}
	}
	return "", err
}

// SetCertificate sets how a domain gets its HTTPS certificate: "disable",
// "eofreecert" (a free certificate EdgeOne applies for and renews), or
// "sslcert" with certificate IDs from the SSL service.
func (c *Client) SetCertificate(ctx context.Context, zoneID, host, mode string, certIDs []string) error {
	in := map[string]any{"ZoneId": zoneID, "Hosts": []string{host}, "Mode": mode}
	if mode == "sslcert" {
		var infos []map[string]string
		for _, id := range certIDs {
			infos = append(infos, map[string]string{"CertId": id})
		}
		in["ServerCertInfo"] = infos
	}
	return c.Call(ctx, "teo", teoVersion, "ModifyHostsCertificate", in, nil)
}

// EdgeOneIPs says which of the IPs (at most 100) are EdgeOne's own nodes.
func (c *Client) EdgeOneIPs(ctx context.Context, ips []string) (map[string]bool, error) {
	var out struct {
		IPRegionInfo []struct {
			IP          string `json:"IP"`
			IsEdgeOneIP string `json:"IsEdgeOneIP"`
		} `json:"IPRegionInfo"`
	}
	if err := c.Call(ctx, "teo", teoVersion, "DescribeIPRegion", map[string]any{"IPs": ips}, &out); err != nil {
		return nil, err
	}
	res := make(map[string]bool, len(ips))
	for _, r := range out.IPRegionInfo {
		res[r.IP] = r.IsEdgeOneIP == "yes"
	}
	return res, nil
}

// L7Log is one EdgeOne offline log package: an hour of a domain's access
// log, gzipped JSON lines.
type L7Log struct {
	Domain    string `json:"Domain"`
	Area      string `json:"Area"` // mainland, overseas
	Name      string `json:"LogPacketName"`
	URL       string `json:"Url"`
	StartTime string `json:"LogStartTime"`
	EndTime   string `json:"LogEndTime"`
	Size      int64  `json:"Size"`
}

// L7Logs lists a site's offline access log packages between start and end.
func (c *Client) L7Logs(ctx context.Context, zoneID string, start, end time.Time) ([]L7Log, error) {
	var all []L7Log
	for offset := 0; ; offset += 300 {
		var out struct {
			TotalCount int     `json:"TotalCount"`
			Data       []L7Log `json:"Data"`
		}
		err := c.Call(ctx, "teo", teoVersion, "DownloadL7Logs", map[string]any{
			"StartTime": start.Format(time.RFC3339), "EndTime": end.Format(time.RFC3339),
			"ZoneIds": []string{zoneID}, "Limit": 300, "Offset": offset,
		}, &out)
		if err != nil {
			return all, err
		}
		all = append(all, out.Data...)
		if len(out.Data) == 0 || offset+len(out.Data) >= out.TotalCount {
			return all, nil
		}
	}
}
