package tencent

import (
	"context"
)

const sslVersion = "2019-12-05"

// SSLCert is a certificate in Tencent Cloud's SSL certificate service.
type SSLCert struct {
	ID            string   `json:"CertificateId"`
	Domain        string   `json:"Domain"`
	SANs          []string `json:"SubjectAltName"`
	Alias         string   `json:"Alias"`
	From          string   `json:"From"` // trustasia, upload, ...
	Product       string   `json:"ProductZhName"`
	Status        int      `json:"Status"` // 1 issued, 3 expired, 0/4 validating, ...
	StatusName    string   `json:"StatusName"`
	BeginTime     string   `json:"CertBeginTime"` // China time, "2026-01-02 15:04:05"
	EndTime       string   `json:"CertEndTime"`
	IsDV          bool     `json:"IsDv"`
	AutoRenewFlag int      `json:"AutoRenewFlag"`
	HostingStatus *int     `json:"HostingStatus"` // 0/5/10 hosted (renewed and replaced automatically), -1 not
}

// SSLCertificates lists the account's server certificates.
func (c *Client) SSLCertificates(ctx context.Context) ([]SSLCert, error) {
	var out struct {
		Certificates []SSLCert `json:"Certificates"`
	}
	err := c.Call(ctx, "ssl", sslVersion, "DescribeCertificates", map[string]any{"Limit": 1000, "CertificateType": "SVR"}, &out)
	return out.Certificates, err
}

// ApplyFreeCert applies for a free DV certificate (TrustAsia, three
// months) for one name, validated automatically: the domain's DNS has to
// be in DNSPod on this account (DNS_AUTO), and the validation record is
// removed once it is issued. It returns the new certificate's ID.
func (c *Client) ApplyFreeCert(ctx context.Context, domain string) (string, error) {
	var out struct {
		CertificateID string `json:"CertificateId"`
	}
	err := c.Call(ctx, "ssl", sslVersion, "ApplyCertificate", map[string]any{
		"DvAuthMethod": "DNS_AUTO", "DomainName": domain, "DeleteDnsAutoRecord": true, "Alias": "Miao Panel " + domain,
	}, &out)
	return out.CertificateID, err
}

// SSLCertificate reads one certificate (DescribeCertificate): Status 1 is
// issued, 0 and 4 are still being validated, 2 failed.
func (c *Client) SSLCertificate(ctx context.Context, id string) (SSLCert, error) {
	var out SSLCert
	err := c.Call(ctx, "ssl", sslVersion, "DescribeCertificate", map[string]any{"CertificateId": id}, &out)
	return out, err
}
