package tencent

import (
	"context"
	"encoding/json"
	"errors"
)

// SecurityPolicy is a site's Web protection policy (the ZoneDefaultPolicy).
// Rules are kept as the API returned them, so that writing a rule list back
// leaves the rules Miao Panel does not manage exactly as they were.
type SecurityPolicy struct {
	CustomRules       []map[string]any
	RateLimitingRules []map[string]any
	// HTTPDDoS is HttpDDoSProtection: AdaptiveFrequencyControl (the CC
	// protection), ClientFiltering, BandwidthAbuseDefense, SlowAttackDefense.
	HTTPDDoS map[string]any
	// Raw is the whole policy, for showing modules Miao Panel does not edit.
	Raw map[string]any
}

// SecurityPolicy reads a site's policy.
func (c *Client) SecurityPolicy(ctx context.Context, zoneID string) (SecurityPolicy, error) {
	var out struct {
		SecurityPolicy map[string]any `json:"SecurityPolicy"`
	}
	err := c.Call(ctx, "teo", teoVersion, "DescribeSecurityPolicy", map[string]any{"ZoneId": zoneID, "Entity": "ZoneDefaultPolicy"}, &out)
	if err != nil {
		return SecurityPolicy{}, err
	}
	if out.SecurityPolicy == nil {
		return SecurityPolicy{}, errors.New("EdgeOne 没有返回这个站点的安全策略")
	}
	p := SecurityPolicy{Raw: out.SecurityPolicy}
	p.CustomRules = ruleList(out.SecurityPolicy["CustomRules"])
	p.RateLimitingRules = ruleList(out.SecurityPolicy["RateLimitingRules"])
	p.HTTPDDoS, _ = out.SecurityPolicy["HttpDDoSProtection"].(map[string]any)
	return p, nil
}

func ruleList(module any) []map[string]any {
	m, _ := module.(map[string]any)
	list, _ := m["Rules"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, r := range list {
		if rule, ok := r.(map[string]any); ok {
			out = append(out, rule)
		}
	}
	return out
}

func (c *Client) modifyPolicy(ctx context.Context, zoneID string, policy map[string]any) error {
	return c.Call(ctx, "teo", teoVersion, "ModifySecurityPolicy",
		map[string]any{"ZoneId": zoneID, "Entity": "ZoneDefaultPolicy", "SecurityPolicy": policy}, nil)
}

// SetCustomRules replaces the site's custom rules. Rules keep their Id to
// stay as they are; a rule without an Id is added; a rule left out is
// deleted.
func (c *Client) SetCustomRules(ctx context.Context, zoneID string, rules []map[string]any) error {
	if rules == nil {
		rules = []map[string]any{}
	}
	return c.modifyPolicy(ctx, zoneID, map[string]any{"CustomRules": map[string]any{"Rules": rules}})
}

// SetRateLimitingRules replaces the site's rate limiting rules, with the
// same rules about Ids as SetCustomRules.
func (c *Client) SetRateLimitingRules(ctx context.Context, zoneID string, rules []map[string]any) error {
	if rules == nil {
		rules = []map[string]any{}
	}
	return c.modifyPolicy(ctx, zoneID, map[string]any{"RateLimitingRules": map[string]any{"Rules": rules}})
}

// SetHTTPDDoS writes HttpDDoSProtection. Every sub-module is sent as given,
// minus the rule Ids EdgeOne only returns.
func (c *Client) SetHTTPDDoS(ctx context.Context, zoneID string, v map[string]any) error {
	clean := map[string]any{}
	for k, sub := range v {
		if m, ok := sub.(map[string]any); ok {
			m = copyMap(m)
			delete(m, "Id")
			clean[k] = m
		}
	}
	return c.modifyPolicy(ctx, zoneID, map[string]any{"HttpDDoSProtection": clean})
}

func copyMap(m map[string]any) map[string]any {
	data, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

// RuleString reads a string field of a rule.
func RuleString(rule map[string]any, key string) string {
	s, _ := rule[key].(string)
	return s
}

// ActionName is a rule's action: Deny, Monitor, Challenge, BlockIP, ...
// For challenges it is the challenge type, e.g. JSChallenge.
func ActionName(rule map[string]any) string {
	a, _ := rule["Action"].(map[string]any)
	name := RuleString(a, "Name")
	if p, _ := a["ChallengeActionParameters"].(map[string]any); name == "Challenge" && RuleString(p, "ChallengeOption") != "" {
		return RuleString(p, "ChallengeOption")
	}
	return name
}
