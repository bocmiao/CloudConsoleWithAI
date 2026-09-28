package aliyuntest

import (
	"net/url"
	"strings"
)

func (f *Cloud) cdnDomain(host string) *CDNDomain {
	for _, d := range f.CDN {
		if d.Name == host {
			return d
		}
	}
	return nil
}

// tasks makes one task per URL, sharing an ID per domain as the CDN
// merges a second's URLs of one domain, and returns the IDs.
func (f *Cloud) tasks(paths, kind string, folder bool) (string, *apiErr) {
	byDomain := map[string]string{}
	var ids []string
	for _, p := range strings.Split(paths, "\n") {
		p = strings.TrimSpace(p)
		u, err := url.Parse(p)
		if p == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", refuse(400, "InvalidObjectPath.Malformed", "The specified ObjectPath %q is not valid.", p)
		}
		if f.cdnDomain(u.Hostname()) == nil {
			return "", refuse(400, "InvalidDomain.NotFound", "The domain %s is not an accelerated domain of yours.", u.Hostname())
		}
		if folder && !strings.HasSuffix(u.Path, "/") {
			return "", refuse(400, "InvalidObjectPath.Malformed", "A directory must end with /.")
		}
		id, ok := byDomain[u.Hostname()]
		if !ok {
			id = f.id("")
			byDomain[u.Hostname()] = id
			ids = append(ids, id)
		}
		f.Tasks = append(f.Tasks, &CDNTask{ID: id, Path: p, Type: kind, Status: "Refreshing", Created: f.Now()})
	}
	return strings.Join(ids, ","), nil
}

func (f *Cloud) serveCDN(action string, q map[string]string) (map[string]any, *apiErr) {
	switch action {
	case "DescribeUserDomains":
		from, to := f.pageNumber(q, 20, len(f.CDN))
		list := []map[string]any{}
		for i, d := range f.CDN[from:to] {
			list = append(list, map[string]any{"DomainName": d.Name, "Cname": d.Cname, "DomainStatus": d.Status, "CdnType": "web",
				"Coverage": "domestic", "SslProtocol": "off", "GmtCreated": "2025-01-02T03:04:05Z", "GmtModified": "2025-01-03T03:04:05Z",
				"Description": "", "ResourceGroupId": "rg-fake", "Sandbox": "normal", "DomainId": from + i + 1,
				"Sources": map[string]any{"Source": []map[string]any{{"Type": "ipaddr", "Content": d.Origin, "Port": 80, "Priority": "20", "Weight": "10"}}}})
		}
		return map[string]any{"Domains": map[string]any{"PageData": list}, "TotalCount": len(f.CDN), "PageNumber": 1, "PageSize": len(list)}, nil
	case "RefreshObjectCaches":
		kind := q["ObjectType"]
		switch kind {
		case "", "File":
			kind = "file"
		case "Directory":
			kind = "directory"
		case "Regex", "IgnoreParams":
			return nil, refuse(400, "InvalidObjectType", "the fake does not refresh by %s", kind)
		default:
			return nil, refuse(400, "InvalidObjectType.Malformed", "The specified ObjectType is not valid.")
		}
		id, err := f.tasks(q["ObjectPath"], kind, kind == "directory")
		if err != nil {
			return nil, err
		}
		return map[string]any{"RefreshTaskId": id}, nil
	case "PushObjectCache":
		if a := q["Area"]; a != "" && a != "domestic" && a != "overseas" {
			return nil, refuse(400, "InvalidArea", "The specified Area is not valid.")
		}
		id, err := f.tasks(q["ObjectPath"], "preload", false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"PushTaskId": id}, nil
	case "DescribeRefreshTasks":
		var match []*CDNTask
		for _, t := range f.Tasks {
			if q["TaskId"] == "" || q["TaskId"] == t.ID {
				match = append(match, t)
			}
		}
		from, to := f.pageNumber(q, 20, len(match))
		list := []map[string]any{}
		for _, t := range match[from:to] {
			if t.Status == "Refreshing" && t.looked {
				t.Status = "Complete" // done by the second look
			}
			t.looked = true
			status, process := t.Status, "0%"
			if status == "Complete" {
				process = "100%"
			}
			list = append(list, map[string]any{"TaskId": t.ID, "ObjectPath": t.Path, "ObjectType": t.Type, "Status": status,
				"Process": process, "CreationTime": t.Created.UTC().Format("2006-01-02T15:04:05Z"), "Description": ""})
		}
		return map[string]any{"Tasks": map[string]any{"CDNTask": list}, "TotalCount": len(match), "PageNumber": 1, "PageSize": len(list)}, nil
	}
	return nil, refuse(404, "InvalidAction.NotFound", "Specified api is not found.")
}
