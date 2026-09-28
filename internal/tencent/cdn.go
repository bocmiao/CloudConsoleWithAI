package tencent

import (
	"context"
	"strings"
)

// Tencent Cloud CDN (内容分发网络): the accelerated domains, refreshing and
// prefetching their cache, turning them on and off, and their HTTPS
// certificate. CDN is global: calls take no region.

const cdnVersion = "2018-06-06"

// CDNDomain is an accelerated domain.
type CDNDomain struct {
	Domain      string   `json:"domain"`
	Cname       string   `json:"cname"`
	Status      string   `json:"status"`      // online, offline, processing, closing, rejected
	ServiceType string   `json:"serviceType"` // web, download, media
	Area        string   `json:"area"`        // mainland, overseas, global
	Disable     string   `json:"disable,omitempty"`
	Origins     []string `json:"origins"`
	OriginType  string   `json:"originType,omitempty"`
	HTTPS       bool     `json:"https"`
	CertID      string   `json:"certId,omitempty"`
	CertExpires string   `json:"certExpires,omitempty"`
	ForceHTTPS  bool     `json:"forceHttps"`
}

// CDNDomains lists the account's CDN domains with their HTTPS settings
// (DescribeDomainsConfig, 100 a page).
func (c *Client) CDNDomains(ctx context.Context) ([]CDNDomain, error) {
	var all []CDNDomain
	for offset := 0; ; offset += 100 {
		var out struct {
			Domains []struct {
				Domain      string `json:"Domain"`
				Cname       string `json:"Cname"`
				Status      string `json:"Status"`
				ServiceType string `json:"ServiceType"`
				Area        string `json:"Area"`
				Disable     string `json:"Disable"`
				Origin      struct {
					Origins    []string `json:"Origins"`
					OriginType string   `json:"OriginType"`
				} `json:"Origin"`
				Https *struct {
					Switch   string `json:"Switch"`
					CertInfo *struct {
						CertID     string `json:"CertId"`
						ExpireTime string `json:"ExpireTime"`
					} `json:"CertInfo"`
				} `json:"Https"`
				ForceRedirect *struct {
					Switch       string `json:"Switch"`
					RedirectType string `json:"RedirectType"`
				} `json:"ForceRedirect"`
			} `json:"Domains"`
			TotalNumber int `json:"TotalNumber"`
		}
		if err := c.Call(ctx, "cdn", cdnVersion, "DescribeDomainsConfig", map[string]any{"Offset": offset, "Limit": 100}, &out); err != nil {
			return all, err
		}
		for _, d := range out.Domains {
			x := CDNDomain{Domain: strings.ToLower(d.Domain), Cname: d.Cname, Status: d.Status, ServiceType: d.ServiceType, Area: d.Area,
				Origins: d.Origin.Origins, OriginType: d.Origin.OriginType}
			if d.Disable != "" && d.Disable != "normal" {
				x.Disable = d.Disable
			}
			if x.Origins == nil {
				x.Origins = []string{}
			}
			if h := d.Https; h != nil && h.Switch == "on" {
				x.HTTPS = true
				if h.CertInfo != nil {
					x.CertID, x.CertExpires = h.CertInfo.CertID, h.CertInfo.ExpireTime
				}
			}
			if r := d.ForceRedirect; r != nil && r.Switch == "on" && r.RedirectType == "https" {
				x.ForceHTTPS = true
			}
			all = append(all, x)
		}
		if len(out.Domains) < 100 || len(all) >= out.TotalNumber {
			return all, nil
		}
	}
}

// PurgeCDN refreshes cached URLs, or whole directories when dirs is set
// (only what changed at the origin); every entry starts with http(s)://.
func (c *Client) PurgeCDN(ctx context.Context, targets []string, dirs bool) (string, error) {
	var out struct {
		TaskID string `json:"TaskId"`
	}
	var err error
	if dirs {
		err = c.Call(ctx, "cdn", cdnVersion, "PurgePathCache", map[string]any{"Paths": targets, "FlushType": "flush"}, &out)
	} else {
		err = c.Call(ctx, "cdn", cdnVersion, "PurgeUrlsCache", map[string]any{"Urls": targets}, &out)
	}
	return out.TaskID, err
}

// PrefetchCDN loads URLs into the CDN's cache ahead of visitors.
func (c *Client) PrefetchCDN(ctx context.Context, urls []string) (string, error) {
	var out struct {
		TaskID string `json:"TaskId"`
	}
	err := c.Call(ctx, "cdn", cdnVersion, "PushUrlsCache", map[string]any{"Urls": urls}, &out)
	return out.TaskID, err
}

// SetCDNDomain turns a domain's acceleration on or off.
func (c *Client) SetCDNDomain(ctx context.Context, domain string, on bool) error {
	action := "StopCdnDomain"
	if on {
		action = "StartCdnDomain"
	}
	return c.Call(ctx, "cdn", cdnVersion, action, map[string]any{"Domain": domain}, nil)
}

// SetCDNCert turns HTTPS on for a domain with a certificate kept in SSL
// 证书 (its CertId), or off when certID is empty.
func (c *Client) SetCDNCert(ctx context.Context, domain, certID string) error {
	https := map[string]any{"Switch": "off"}
	if certID != "" {
		https = map[string]any{"Switch": "on", "Http2": "on", "CertInfo": map[string]any{"CertId": certID}}
	}
	return c.Call(ctx, "cdn", cdnVersion, "UpdateDomainConfig", map[string]any{"Domain": domain, "Https": https}, nil)
}
