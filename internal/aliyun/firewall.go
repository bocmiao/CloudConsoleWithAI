package aliyun

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// FirewallRule is an inbound rule of a Simple Application Server firewall
// or of an ECS security group.
type FirewallRule struct {
	// Protocol is TCP (when empty), UDP or ICMP; Simple Application Server
	// also has TCP+UDP, ECS also ALL, GRE and ICMPv6.
	Protocol string `json:"protocol"`
	// Port is one port ("22"), a range ("8000-9000"), or empty for all.
	Port string `json:"port"`
	// Source is a CIDR block (0.0.0.0/0 when empty); for ECS it may also
	// be a security group (sg-…) or prefix list (pl-…) ID.
	Source      string `json:"source"`
	Policy      string `json:"policy"` // accept or drop
	Description string `json:"description,omitempty"`
	Priority    int    `json:"priority,omitempty"` // ECS only: 1 (first) to 100
	// ID is what deleting the rule takes: the firewall rule ID, or the
	// security group rule ID; Group is the ECS rule's security group.
	ID    string `json:"id,omitempty"`
	Group string `json:"group,omitempty"`
}

// ports reads the API's "22/22", "1/200", "-1/-1" or "3306" as Port.
func ports(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "-1/-1" || strings.EqualFold(p, "all") {
		return ""
	}
	if from, to, ok := strings.Cut(p, "/"); ok {
		if from == to {
			return from
		}
		return from + "-" + to
	}
	return p
}

// apiPorts writes Port the way the APIs take it: ECS always as a range
// ("22/22"), Simple Application Server a single port as it is.
func apiPorts(p string, ecs bool) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "-1/-1" || strings.EqualFold(p, "all") {
		return "-1/-1"
	}
	p = strings.Replace(p, "-", "/", 1)
	if !strings.Contains(p, "/") && ecs {
		return p + "/" + p
	}
	return p
}

func protocol(p string) string {
	if p == "" {
		return "TCP"
	}
	return strings.ToUpper(p)
}

func policy(p string) string {
	if p == "" {
		return "accept"
	}
	return strings.ToLower(p)
}

// SWASFirewall lists the rules of a Simple Application Server's firewall.
func (c *Client) SWASFirewall(ctx context.Context, region, id string) ([]FirewallRule, error) {
	var rules []FirewallRule
	for page := 1; ; page++ {
		var out struct {
			FirewallRules []struct {
				RuleID       string `json:"RuleId"`
				RuleProtocol string `json:"RuleProtocol"`
				Port         string `json:"Port"`
				SourceCidrIP string `json:"SourceCidrIp"`
				Policy       string `json:"Policy"`
				Remark       string `json:"Remark"`
			} `json:"FirewallRules"`
			TotalCount int `json:"TotalCount"`
		}
		err := c.Call(ctx, ProductSWAS, VersionSWAS, "ListFirewallRules", region,
			map[string]string{"InstanceId": id, "PageSize": "100", "PageNumber": strconv.Itoa(page)}, &out)
		if err != nil {
			return rules, err
		}
		for _, r := range out.FirewallRules {
			src := r.SourceCidrIP
			if src == "" {
				src = "0.0.0.0/0"
			}
			rules = append(rules, FirewallRule{Protocol: strings.ToUpper(r.RuleProtocol), Port: ports(r.Port), Source: src,
				Policy: policy(r.Policy), Description: r.Remark, ID: r.RuleID})
		}
		if len(out.FirewallRules) == 0 || len(rules) >= out.TotalCount {
			return rules, nil
		}
	}
}

// AddSWASFirewall adds rules to a Simple Application Server's firewall
// and returns their IDs. Its firewall only allows traffic, so a drop rule
// is refused here.
func (c *Client) AddSWASFirewall(ctx context.Context, region, id string, rules ...FirewallRule) ([]string, error) {
	type rule struct {
		RuleProtocol string `json:"RuleProtocol"`
		Port         string `json:"Port"`
		SourceCidrIP string `json:"SourceCidrIp,omitempty"`
		Remark       string `json:"Remark,omitempty"`
	}
	var list []rule
	for _, r := range rules {
		if policy(r.Policy) != "accept" {
			return nil, errors.New("轻量应用服务器的防火墙只能添加放行规则，不能添加拒绝规则")
		}
		src := r.Source
		if src == "" {
			src = "0.0.0.0/0"
		}
		list = append(list, rule{RuleProtocol: protocol(r.Protocol), Port: apiPorts(r.Port, false), SourceCidrIP: src, Remark: r.Description})
	}
	b, _ := json.Marshal(list)
	var out struct {
		FirewallRuleIDs []string `json:"FirewallRuleIds"`
	}
	err := c.Call(ctx, ProductSWAS, VersionSWAS, "CreateFirewallRules", region, map[string]string{"InstanceId": id, "FirewallRules": string(b)}, &out)
	return out.FirewallRuleIDs, err
}

// DeleteSWASFirewall removes rules from a Simple Application Server's
// firewall by their IDs.
func (c *Client) DeleteSWASFirewall(ctx context.Context, region, id string, ruleIDs ...string) error {
	return c.Call(ctx, ProductSWAS, VersionSWAS, "DeleteFirewallRules", region,
		map[string]string{"InstanceId": id, "RuleIds": strings.Join(ruleIDs, ",")}, nil)
}

// SecurityGroupIngress lists the inbound rules of an ECS security group.
func (c *Client) SecurityGroupIngress(ctx context.Context, region, group string) ([]FirewallRule, error) {
	var rules []FirewallRule
	for token := ""; ; {
		in := map[string]string{"SecurityGroupId": group, "Direction": "ingress", "MaxResults": "1000"}
		if token != "" {
			in["NextToken"] = token
		}
		var out struct {
			Permissions struct {
				Permission []struct {
					SecurityGroupRuleID string `json:"SecurityGroupRuleId"`
					Direction           string `json:"Direction"`
					IPProtocol          string `json:"IpProtocol"`
					PortRange           string `json:"PortRange"`
					SourceCidrIP        string `json:"SourceCidrIp"`
					Ipv6SourceCidrIP    string `json:"Ipv6SourceCidrIp"`
					SourceGroupID       string `json:"SourceGroupId"`
					SourcePrefixListID  string `json:"SourcePrefixListId"`
					Policy              string `json:"Policy"`
					Priority            number `json:"Priority"`
					Description         string `json:"Description"`
				} `json:"Permission"`
			} `json:"Permissions"`
			NextToken string `json:"NextToken"`
		}
		if err := c.Call(ctx, ProductECS, VersionECS, "DescribeSecurityGroupAttribute", region, in, &out); err != nil {
			return rules, err
		}
		for _, p := range out.Permissions.Permission {
			if p.Direction != "" && p.Direction != "ingress" {
				continue
			}
			src := p.SourceCidrIP
			for _, alt := range []string{p.Ipv6SourceCidrIP, p.SourceGroupID, p.SourcePrefixListID} {
				if src == "" {
					src = alt
				}
			}
			rules = append(rules, FirewallRule{Protocol: strings.ToUpper(p.IPProtocol), Port: ports(p.PortRange), Source: src,
				Policy: policy(p.Policy), Priority: int(p.Priority), Description: p.Description, ID: p.SecurityGroupRuleID, Group: group})
		}
		if out.NextToken == "" || out.NextToken == token || len(out.Permissions.Permission) == 0 {
			return rules, nil
		}
		token = out.NextToken
	}
}

// AuthorizeIngress adds inbound rules to an ECS security group.
func (c *Client) AuthorizeIngress(ctx context.Context, region, group string, rules ...FirewallRule) error {
	in := map[string]string{"SecurityGroupId": group}
	for i, r := range rules {
		k := "Permissions." + strconv.Itoa(i+1) + "."
		in[k+"IpProtocol"] = protocol(r.Protocol)
		in[k+"PortRange"] = apiPorts(r.Port, true)
		in[k+"Policy"] = policy(r.Policy)
		switch src := r.Source; {
		case strings.HasPrefix(src, "sg-"):
			in[k+"SourceGroupId"] = src
		case strings.HasPrefix(src, "pl-"):
			in[k+"SourcePrefixListId"] = src
		case strings.Contains(src, ":"):
			in[k+"Ipv6SourceCidrIp"] = src
		case src == "":
			in[k+"SourceCidrIp"] = "0.0.0.0/0"
		default:
			in[k+"SourceCidrIp"] = src
		}
		if r.Priority > 0 {
			in[k+"Priority"] = strconv.Itoa(r.Priority)
		}
		if r.Description != "" {
			in[k+"Description"] = r.Description
		}
	}
	return c.Call(ctx, ProductECS, VersionECS, "AuthorizeSecurityGroup", region, in, nil)
}

// RevokeIngress removes rules from an ECS security group by their IDs.
func (c *Client) RevokeIngress(ctx context.Context, region, group string, ruleIDs ...string) error {
	in := map[string]string{"SecurityGroupId": group}
	for i, id := range ruleIDs {
		in["SecurityGroupRuleId."+strconv.Itoa(i+1)] = id
	}
	return c.Call(ctx, ProductECS, VersionECS, "RevokeSecurityGroup", region, in, nil)
}

// Firewall lists a server's inbound rules: a Simple Application Server's
// firewall, or the rules of every security group of an ECS instance.
func (c *Client) Firewall(ctx context.Context, s Server) ([]FirewallRule, error) {
	if s.Kind == KindSWAS {
		return c.SWASFirewall(ctx, s.Region, s.ID)
	}
	var all []FirewallRule
	for _, g := range s.SecurityGroups {
		rules, err := c.SecurityGroupIngress(ctx, s.Region, g)
		all = append(all, rules...)
		if err != nil {
			return all, err
		}
	}
	return all, nil
}

// AddFirewallRule adds an inbound rule for a server; for ECS it goes into
// r.Group, or the instance's first security group.
func (c *Client) AddFirewallRule(ctx context.Context, s Server, r FirewallRule) error {
	if s.Kind == KindSWAS {
		_, err := c.AddSWASFirewall(ctx, s.Region, s.ID, r)
		return err
	}
	group, err := groupOf(s, r)
	if err != nil {
		return err
	}
	return c.AuthorizeIngress(ctx, s.Region, group, r)
}

// DeleteFirewallRule removes a rule Firewall listed.
func (c *Client) DeleteFirewallRule(ctx context.Context, s Server, r FirewallRule) error {
	if r.ID == "" {
		return errors.New("这条规则没有 ID，删除不了；请先重新读取防火墙规则")
	}
	if s.Kind == KindSWAS {
		return c.DeleteSWASFirewall(ctx, s.Region, s.ID, r.ID)
	}
	group, err := groupOf(s, r)
	if err != nil {
		return err
	}
	return c.RevokeIngress(ctx, s.Region, group, r.ID)
}

func groupOf(s Server, r FirewallRule) (string, error) {
	if r.Group != "" {
		return r.Group, nil
	}
	if len(s.SecurityGroups) == 0 {
		return "", errors.New("这台 ECS 没有加入任何安全组")
	}
	return s.SecurityGroups[0], nil
}
