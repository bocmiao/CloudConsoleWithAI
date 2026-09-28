package aliyun

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The account itself: the money in it, the domains registered with it, and
// whether an ECS subscription renews itself.

// Balance is what the account holds, in yuan.
type Balance struct {
	Available float64 `json:"available"` // can be spent now
	Cash      float64 `json:"cash"`
	Credit    float64 `json:"credit"`
	Currency  string  `json:"currency"`
}

// Balance asks 费用中心 for the account's money (QueryAccountBalance).
func (c *Client) Balance(ctx context.Context) (Balance, error) {
	var out struct {
		Data struct {
			AvailableAmount     string `json:"AvailableAmount"`
			AvailableCashAmount string `json:"AvailableCashAmount"`
			CreditAmount        string `json:"CreditAmount"`
			Currency            string `json:"Currency"`
		} `json:"Data"`
	}
	if err := c.Call(ctx, ProductBSS, VersionBSS, "QueryAccountBalance", "", map[string]string{}, &out); err != nil {
		return Balance{}, err
	}
	yuan := func(s string) float64 {
		v, _ := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", ""), 64)
		return v
	}
	return Balance{Available: yuan(out.Data.AvailableAmount), Cash: yuan(out.Data.AvailableCashAmount),
		Credit: yuan(out.Data.CreditAmount), Currency: out.Data.Currency}, nil
}

// RegisteredDomain is a domain registered with 阿里云.
type RegisteredDomain struct {
	Name    string `json:"name"`
	Expires string `json:"expires"` // 2027-05-10
}

// RegisteredDomains lists the domains registered with the account
// (QueryDomainList); domains only hosted in 云解析 are not there.
func (c *Client) RegisteredDomains(ctx context.Context) ([]RegisteredDomain, error) {
	var all []RegisteredDomain
	for page := 1; ; page++ {
		var out struct {
			TotalItemNum int `json:"TotalItemNum"`
			Data         struct {
				Domain []struct {
					DomainName     string `json:"DomainName"`
					ExpirationDate string `json:"ExpirationDate"`
				} `json:"Domain"`
			} `json:"Data"`
		}
		err := c.Call(ctx, ProductDomain, VersionDomain, "QueryDomainList", "",
			map[string]string{"PageNum": strconv.Itoa(page), "PageSize": "100"}, &out)
		if err != nil {
			return all, err
		}
		for _, d := range out.Data.Domain {
			all = append(all, RegisteredDomain{Name: strings.ToLower(d.DomainName), Expires: dateOnly(d.ExpirationDate)})
		}
		if len(out.Data.Domain) < 100 || len(all) >= out.TotalItemNum {
			return all, nil
		}
	}
}

// SetAutoRenew turns an ECS subscription's automatic renewal on (a month
// at a time) or off (ModifyInstanceAutoRenewAttribute). Simple
// Application Servers renew in their own console.
func (c *Client) SetAutoRenew(ctx context.Context, region, id string, on bool) error {
	if KindOf(id) == KindSWAS {
		return fmt.Errorf("轻量应用服务器的自动续费请在阿里云控制台设置")
	}
	in := map[string]string{"InstanceId": id, "AutoRenew": strconv.FormatBool(on), "RenewalStatus": "Normal"}
	if on {
		in["Duration"], in["PeriodUnit"], in["RenewalStatus"] = "1", "Month", "AutoRenewal"
	}
	return c.Call(ctx, ProductECS, VersionECS, "ModifyInstanceAutoRenewAttribute", region, in, nil)
}

// dateOnly keeps the day of a time the APIs write in one of their ways.
func dateOnly(s string) string {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04Z", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return s
}
