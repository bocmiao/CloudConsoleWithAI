package tencent

import (
	"context"
	"strings"
	"time"
)

// The account itself: the money in it, the domains registered with it, and
// whether a server renews itself.

const (
	billingVersion = "2018-07-09"
	domainVersion  = "2018-08-08"
)

// Balance is what the account holds, in yuan.
type Balance struct {
	Available float64 `json:"available"` // can be spent now (cash and gifts)
	Cash      float64 `json:"cash"`
	Owed      float64 `json:"owed"`   // overdue bills
	Frozen    float64 `json:"frozen"` // held for orders
}

// Balance asks for the account's money (DescribeAccountBalance, in fen).
func (c *Client) Balance(ctx context.Context) (Balance, error) {
	var out struct {
		Balance            float64  `json:"Balance"`
		RealBalance        *float64 `json:"RealBalance"`
		CashAccountBalance float64  `json:"CashAccountBalance"`
		OweAmount          float64  `json:"OweAmount"`
		FreezeAmount       float64  `json:"FreezeAmount"`
	}
	if err := c.Call(ctx, "billing", billingVersion, "DescribeAccountBalance", map[string]any{}, &out); err != nil {
		return Balance{}, err
	}
	fen := out.Balance
	if out.RealBalance != nil {
		fen = *out.RealBalance
	}
	return Balance{Available: fen / 100, Cash: out.CashAccountBalance / 100, Owed: out.OweAmount / 100, Frozen: out.FreezeAmount / 100}, nil
}

// RegisteredDomain is a domain registered with Tencent Cloud.
type RegisteredDomain struct {
	Name      string `json:"name"`
	Expires   string `json:"expires"` // 2027-05-10
	AutoRenew bool   `json:"autoRenew"`
}

// RegisteredDomains lists the domains registered with the account
// (DescribeDomainNameList); domains only hosted in DNSPod are not there.
func (c *Client) RegisteredDomains(ctx context.Context) ([]RegisteredDomain, error) {
	var all []RegisteredDomain
	for offset := 0; ; offset += 100 {
		var out struct {
			DomainSet []struct {
				DomainName     string `json:"DomainName"`
				ExpirationDate string `json:"ExpirationDate"`
				AutoRenew      int    `json:"AutoRenew"` // 0 manual, 1 automatic, 2 let it lapse
			} `json:"DomainSet"`
			TotalCount int `json:"TotalCount"`
		}
		if err := c.Call(ctx, "domain", domainVersion, "DescribeDomainNameList", map[string]any{"Offset": offset, "Limit": 100}, &out); err != nil {
			return all, err
		}
		for _, d := range out.DomainSet {
			all = append(all, RegisteredDomain{Name: strings.ToLower(d.DomainName), Expires: dateOnly(d.ExpirationDate), AutoRenew: d.AutoRenew == 1})
		}
		if len(out.DomainSet) < 100 || len(all) >= out.TotalCount {
			return all, nil
		}
	}
}

// Renew flags of Lighthouse and CVM instances.
const (
	RenewAuto   = "NOTIFY_AND_AUTO_RENEW"
	RenewManual = "NOTIFY_AND_MANUAL_RENEW"
)

// SetRenewFlag sets how a prepaid instance renews: RenewAuto or
// RenewManual (ModifyInstancesRenewFlag).
func (c *Client) SetRenewFlag(ctx context.Context, region, id, flag string) error {
	in := map[string]any{"InstanceIds": []string{id}, "RenewFlag": flag}
	if KindOf(id) == Lighthouse {
		return c.CallRegion(ctx, Lighthouse, lighthouseVersion, "ModifyInstancesRenewFlag", region, in, nil)
	}
	return c.CallRegion(ctx, CVM, cvmVersion, "ModifyInstancesRenewFlag", region, in, nil)
}

// dateOnly keeps the day of a time the APIs write in one of their ways.
func dateOnly(s string) string {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return s
}
