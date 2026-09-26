package onepanel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SSL is a certificate kept by 1Panel (网站 → 证书). The private key is
// deliberately not part of this type.
type SSL struct {
	ID            uint      `json:"id"`
	PrimaryDomain string    `json:"primaryDomain"`
	Domains       string    `json:"domains"`  // other domains, comma separated
	Provider      string    `json:"provider"` // dnsAccount, http, dnsManual, manual, selfSigned, ...
	Status        string    `json:"status"`   // init, applying, ready, applyError, error
	Message       string    `json:"message"`
	AutoRenew     bool      `json:"autoRenew"`
	ExpireDate    time.Time `json:"expireDate"`
	StartDate     time.Time `json:"startDate"`
	Type          string    `json:"type"`
	Organization  string    `json:"organization"`
	KeyType       string    `json:"keyType"`
	AcmeAccountID uint      `json:"acmeAccountId"`
	DnsAccountID  uint      `json:"dnsAccountId"`
	Description   string    `json:"description"`
	PushDir       bool      `json:"pushDir"`
	Dir           string    `json:"dir"`
	Websites      []struct {
		ID            uint   `json:"id"`
		PrimaryDomain string `json:"primaryDomain"`
	} `json:"websites"`
}

// Names lists every domain the certificate covers.
func (s SSL) Names() []string {
	names := []string{s.PrimaryDomain}
	for _, d := range strings.Split(s.Domains, ",") {
		if d = strings.TrimSpace(d); d != "" {
			names = append(names, d)
		}
	}
	return names
}

// SSLs lists the panel's certificates.
func (c *Client) SSLs(ctx context.Context) ([]SSL, error) {
	var page struct {
		Items []SSL `json:"items"`
	}
	err := c.do(ctx, http.MethodPost, "/websites/ssl/search", map[string]any{"page": 1, "pageSize": 500}, &page)
	return page.Items, err
}

// SSL returns one certificate.
func (c *Client) SSL(ctx context.Context, id uint) (SSL, error) {
	var s SSL
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/websites/ssl/%d", id), nil, &s)
	return s, err
}

// Account is an ACME account or a DNS account kept by the panel.
type Account struct {
	ID    uint   `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Type  string `json:"type"` // letsencrypt, zerossl, ... / TencentCloud, AliYun, ...
}

func (c *Client) accounts(ctx context.Context, path string) ([]Account, error) {
	var page struct {
		Items []Account `json:"items"`
	}
	err := c.do(ctx, http.MethodPost, path, map[string]any{"page": 1, "pageSize": 100}, &page)
	return page.Items, err
}

// AcmeAccounts lists the ACME (certificate authority) accounts.
func (c *Client) AcmeAccounts(ctx context.Context) ([]Account, error) {
	return c.accounts(ctx, "/websites/acme/search")
}

// DNSAccounts lists the DNS provider accounts used for DNS validation.
func (c *Client) DNSAccounts(ctx context.Context) ([]Account, error) {
	return c.accounts(ctx, "/websites/dns/search")
}

// CreateAcmeAccount registers a Let's Encrypt account for email.
func (c *Client) CreateAcmeAccount(ctx context.Context, email string) error {
	in := map[string]any{"email": email, "type": "letsencrypt", "keyType": "EC256"}
	err := c.do(ctx, http.MethodPost, "/websites/acme", in, nil)
	var pe *Error
	if errors.As(err, &pe) && strings.Contains(pe.Message, "KeyType") {
		in["keyType"] = "P256" // panels built on the older ACME library
		err = c.do(ctx, http.MethodPost, "/websites/acme", in, nil)
	}
	return err
}

// NewSSL asks the panel for a certificate from its ACME account.
type NewSSL struct {
	Domain       string
	OtherDomains []string
	Provider     string // http or dnsAccount
	AcmeAccount  uint
	DNSAccount   uint
	AutoRenew    bool
	Description  string
}

// CreateSSL starts getting a certificate; the panel works on it in the
// background, so poll SSL until its status is ready or applyError.
func (c *Client) CreateSSL(ctx context.Context, n NewSSL) (uint, error) {
	in := map[string]any{
		"primaryDomain": n.Domain, "otherDomains": strings.Join(n.OtherDomains, "\n"), "provider": n.Provider,
		"acmeAccountId": n.AcmeAccount, "dnsAccountId": n.DNSAccount, "autoRenew": n.AutoRenew,
		"keyType": "P256", "description": n.Description,
	}
	var out struct {
		ID uint `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/websites/ssl", in, &out)
	return out.ID, err
}

// RenewSSL gets a fresh certificate for an existing one now.
func (c *Client) RenewSSL(ctx context.Context, id uint) error {
	return c.do(ctx, http.MethodPost, "/websites/ssl/obtain", map[string]any{"ID": id}, nil)
}

// SetSSLAutoRenew switches automatic renewal, keeping everything else.
func (c *Client) SetSSLAutoRenew(ctx context.Context, s SSL, on bool) error {
	return c.do(ctx, http.MethodPost, "/websites/ssl/update", map[string]any{
		"id": s.ID, "autoRenew": on, "description": s.Description, "primaryDomain": s.PrimaryDomain,
		"otherDomains": strings.ReplaceAll(s.Domains, ",", "\n"), "provider": s.Provider, "acmeAccountId": s.AcmeAccountID,
		"dnsAccountId": s.DnsAccountID, "keyType": s.KeyType, "pushDir": s.PushDir, "dir": s.Dir,
	}, nil)
}

// DeleteSSL removes a certificate.
func (c *Client) DeleteSSL(ctx context.Context, id uint) error {
	return c.do(ctx, http.MethodPost, "/websites/ssl/del", map[string]any{"ids": []uint{id}}, nil)
}

// HTTPS is a website's HTTPS setting.
type HTTPS struct {
	Enable      bool     `json:"enable"`
	HTTPConfig  string   `json:"httpConfig"` // HTTPSOnly, HTTPAlso, HTTPToHTTPS
	SSLProtocol []string `json:"SSLProtocol"`
	Algorithm   string   `json:"algorithm"`
	Hsts        bool     `json:"hsts"`
	Http3       bool     `json:"http3"`
	SSL         struct {
		ID         uint      `json:"id"`
		ExpireDate time.Time `json:"expireDate"`
	} `json:"SSL"`
}

// WebsiteHTTPS reads a website's HTTPS setting.
func (c *Client) WebsiteHTTPS(ctx context.Context, websiteID uint) (HTTPS, error) {
	var h HTTPS
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/websites/%d/https", websiteID), nil, &h)
	return h, err
}

// DefaultAlgorithm is the cipher list 1Panel itself uses.
const DefaultAlgorithm = "ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305:ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:DHE-RSA-AES256-GCM-SHA384:DHE-RSA-AES128-GCM-SHA256:ECDHE-RSA-AES256-SHA384:ECDHE-RSA-AES128-SHA256:!aNULL:!eNULL:!EXPORT:!DSS:!DES:!RC4:!3DES:!MD5:!PSK:!KRB5:!SRP:!CAMELLIA:!SEED"

// SetWebsiteHTTPS turns HTTPS on with one of the panel's certificates, or
// off when h.Enable is false.
func (c *Client) SetWebsiteHTTPS(ctx context.Context, websiteID uint, h HTTPS) error {
	in := map[string]any{"websiteId": websiteID, "enable": h.Enable, "type": "existed", "httpConfig": h.HTTPConfig}
	if h.HTTPConfig == "" {
		in["httpConfig"] = "HTTPAlso"
	}
	if h.Enable {
		protocols, algorithm := h.SSLProtocol, h.Algorithm
		if len(protocols) == 0 {
			protocols = []string{"TLSv1.3", "TLSv1.2"}
		}
		if algorithm == "" {
			algorithm = DefaultAlgorithm
		}
		in["websiteSSLId"], in["SSLProtocol"], in["algorithm"], in["hsts"], in["http3"] = h.SSL.ID, protocols, algorithm, h.Hsts, h.Http3
	}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/websites/%d/https", websiteID), in, nil)
}
