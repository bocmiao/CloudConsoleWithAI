// Package certs reads TLS certificates: the one a site actually serves,
// or one given as PEM.
package certs

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"strings"
	"time"
)

// Info describes one certificate.
type Info struct {
	Names     []string  `json:"names"`
	Issuer    string    `json:"issuer"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	SHA256    string    `json:"sha256"`
	// Valid says whether the certificate chains to a trusted root and
	// covers the name it was fetched for; Problem says why not.
	Valid   bool   `json:"valid"`
	Problem string `json:"problem,omitempty"`
}

// DaysLeft is how many whole days remain until the certificate expires.
func (i Info) DaysLeft(now time.Time) int {
	return int(i.NotAfter.Sub(now).Hours() / 24)
}

func issuerName(c *x509.Certificate) string {
	if len(c.Issuer.Organization) > 0 {
		name := c.Issuer.Organization[0]
		if c.Issuer.CommonName != "" && !strings.Contains(name, c.Issuer.CommonName) {
			name += " " + c.Issuer.CommonName
		}
		return name
	}
	return c.Issuer.CommonName
}

func infoOf(leaf *x509.Certificate) Info {
	names := append([]string(nil), leaf.DNSNames...)
	if len(names) == 0 && leaf.Subject.CommonName != "" {
		names = []string{leaf.Subject.CommonName}
	}
	sum := sha256.Sum256(leaf.Raw)
	return Info{Names: names, Issuer: issuerName(leaf), NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, SHA256: hex.EncodeToString(sum[:])}
}

// Probe connects to addr (host:443 when empty) and reports the certificate
// it serves for host. Roots overrides the system roots (tests).
func Probe(ctx context.Context, host, addr string, roots *x509.CertPool) (Info, error) {
	if addr == "" {
		addr = net.JoinHostPort(host, "443")
	}
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 8 * time.Second}, Config: &tls.Config{
		ServerName: host,
		// Read the certificate even when it is invalid; it is verified below.
		InsecureSkipVerify: true, //nolint:gosec
	}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Info{}, err
	}
	defer conn.Close()
	state := conn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return Info{}, errors.New("没有收到证书")
	}
	leaf := state.PeerCertificates[0]
	info := infoOf(leaf)
	inter := x509.NewCertPool()
	for _, c := range state.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter, Roots: roots})
	info.Valid = verr == nil
	if verr != nil {
		info.Problem = explain(verr)
	}
	return info, nil
}

// explain turns a verification error into plain words.
func explain(err error) string {
	var inv x509.CertificateInvalidError
	var host x509.HostnameError
	var unknown x509.UnknownAuthorityError
	switch {
	case errors.As(err, &inv) && inv.Reason == x509.Expired:
		return "证书已过期（或还没生效）"
	case errors.As(err, &host):
		return "证书不包含这个域名"
	case errors.As(err, &unknown):
		return "证书不是受信任的机构签发的（可能是自签名证书，或者缺少中间证书）"
	}
	return err.Error()
}

// Parse reads the first certificate in PEM data.
func Parse(data string) (Info, error) {
	for rest := []byte(data); ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			return Info{}, errors.New("没有找到证书")
		}
		if b.Type == "CERTIFICATE" {
			c, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				return Info{}, err
			}
			return infoOf(c), nil
		}
	}
}
