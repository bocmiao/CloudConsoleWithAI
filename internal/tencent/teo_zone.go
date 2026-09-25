package tencent

import (
	"context"
)

// Plan is an EdgeOne plan (套餐). A site has to be bound to one.
type Plan struct {
	PlanID      string `json:"PlanId"`
	PlanType    string `json:"PlanType"` // plan-trial, plan-personal, plan-basic, plan-standard, plan-enterprise
	Area        string `json:"Area"`     // mainland, overseas, global
	Status      string `json:"Status"`   // normal, expiring-soon, expired, isolated, overdue-isolated
	Bindable    string `json:"Bindable"` // "true" when it can take another site
	ExpiredTime string `json:"ExpiredTime"`
	ZonesInfo   []struct {
		ZoneID   string `json:"ZoneId"`
		ZoneName string `json:"ZoneName"`
		Paused   bool   `json:"Paused"`
	} `json:"ZonesInfo"`
}

// Plans lists the account's EdgeOne plans.
func (c *Client) Plans(ctx context.Context) ([]Plan, error) {
	var out struct {
		Plans []Plan `json:"Plans"`
	}
	err := c.Call(ctx, "teo", teoVersion, "DescribePlans", map[string]any{"Limit": 200}, &out)
	return out.Plans, err
}

// CreateZone adds a site with CNAME access, bound to a plan. For sites
// that still have to prove the domain is yours, it returns the TXT record
// to add.
func (c *Client) CreateZone(ctx context.Context, name, area, planID string) (string, *DNSVerification, error) {
	var out struct {
		ZoneID                string     `json:"ZoneId"`
		OwnershipVerification *Ownership `json:"OwnershipVerification"`
	}
	err := c.Call(ctx, "teo", teoVersion, "CreateZone", map[string]any{
		"Type": "partial", "ZoneName": name, "Area": area, "PlanId": planID,
	}, &out)
	if ov := out.OwnershipVerification; ov != nil && ov.DNSVerification != nil && ov.DNSVerification.RecordValue != "" {
		return out.ZoneID, ov.DNSVerification, err
	}
	return out.ZoneID, nil, err
}

// VerifyOwnership asks EdgeOne to check the verification record now. It
// returns "success" or "fail" with the reason.
func (c *Client) VerifyOwnership(ctx context.Context, domain string) (string, string, error) {
	var out struct {
		Status string `json:"Status"`
		Result string `json:"Result"`
	}
	err := c.Call(ctx, "teo", teoVersion, "VerifyOwnership", map[string]any{"Domain": domain}, &out)
	return out.Status, out.Result, err
}

// SetZonePaused switches a whole site off (true) or back on.
func (c *Client) SetZonePaused(ctx context.Context, zoneID string, paused bool) error {
	return c.Call(ctx, "teo", teoVersion, "ModifyZoneStatus", map[string]any{"ZoneId": zoneID, "Paused": paused}, nil)
}

// DeleteZone removes a site; it has to be switched off first.
func (c *Client) DeleteZone(ctx context.Context, zoneID string) error {
	return c.Call(ctx, "teo", teoVersion, "DeleteZone", map[string]any{"ZoneId": zoneID}, nil)
}
