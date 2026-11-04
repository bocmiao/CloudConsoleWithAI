package aliyun

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// CDNDomain is an accelerated domain on Alibaba Cloud CDN.
type CDNDomain struct {
	Name string `json:"name"`
	// CNAME is where the domain's DNS must point for traffic to go
	// through the CDN.
	CNAME string `json:"cname"`
	// Status is online, offline, configuring, configure_failed, checking,
	// check_failed, stopping or deleting.
	Status   string      `json:"status"`
	Type     string      `json:"type"`     // web, download or video
	Coverage string      `json:"coverage"` // domestic, overseas or global
	HTTPS    bool        `json:"https"`
	Created  string      `json:"created"`
	Modified string      `json:"modified"`
	Remark   string      `json:"remark,omitempty"`
	Sources  []CDNSource `json:"sources"`
}

// CDNSource is one origin of an accelerated domain.
type CDNSource struct {
	Type     string `json:"type"` // ipaddr, domain or oss
	Content  string `json:"content"`
	Port     int    `json:"port"`
	Priority string `json:"priority,omitempty"`
	Weight   string `json:"weight,omitempty"`
}

// CDNDomains lists every accelerated domain of the account, 500 a page.
func (c *Client) CDNDomains(ctx context.Context) ([]CDNDomain, error) {
	var list []CDNDomain
	for page := 1; ; page++ {
		var out struct {
			Domains struct {
				PageData []struct {
					DomainName   string `json:"DomainName"`
					Cname        string `json:"Cname"`
					DomainStatus string `json:"DomainStatus"`
					CdnType      string `json:"CdnType"`
					Coverage     string `json:"Coverage"`
					SslProtocol  string `json:"SslProtocol"`
					GmtCreated   string `json:"GmtCreated"`
					GmtModified  string `json:"GmtModified"`
					Description  string `json:"Description"`
					Sources      struct {
						Source []struct {
							Type     string `json:"Type"`
							Content  string `json:"Content"`
							Port     int    `json:"Port"`
							Priority text   `json:"Priority"`
							Weight   text   `json:"Weight"`
						} `json:"Source"`
					} `json:"Sources"`
				} `json:"PageData"`
			} `json:"Domains"`
			TotalCount int `json:"TotalCount"`
		}
		err := c.Call(ctx, ProductCDN, VersionCDN, "DescribeUserDomains", "", map[string]string{"PageNumber": strconv.Itoa(page), "PageSize": "500"}, &out)
		if err != nil {
			return list, err
		}
		for _, d := range out.Domains.PageData {
			cd := CDNDomain{Name: d.DomainName, CNAME: d.Cname, Status: d.DomainStatus, Type: d.CdnType, Coverage: d.Coverage,
				HTTPS: d.SslProtocol == "on", Created: rfc3339(d.GmtCreated), Modified: rfc3339(d.GmtModified), Remark: d.Description}
			for _, s := range d.Sources.Source {
				cd.Sources = append(cd.Sources, CDNSource{Type: s.Type, Content: s.Content, Port: s.Port, Priority: string(s.Priority), Weight: string(s.Weight)})
			}
			list = append(list, cd)
		}
		if len(out.Domains.PageData) == 0 || len(list) >= out.TotalCount {
			return list, nil
		}
	}
}

// taskIDs splits the comma-separated task IDs the purge and prefetch
// calls answer with.
func taskIDs(s string) []string {
	var ids []string
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// PurgeCDN refreshes cached content: full URLs, or with directory whole
// folders (each ending in "/"). It returns the task IDs to follow with
// CDNTasks; the CDN merges the URLs of one domain sent in the same
// second into one task.
func (c *Client) PurgeCDN(ctx context.Context, paths []string, directory bool) ([]string, error) {
	if len(paths) == 0 {
		return nil, errors.New("没有要刷新的地址")
	}
	kind := "File"
	if directory {
		kind = "Directory"
	}
	var out struct {
		RefreshTaskID string `json:"RefreshTaskId"`
	}
	err := c.Call(ctx, ProductCDN, VersionCDN, "RefreshObjectCaches", "", map[string]string{"ObjectPath": strings.Join(paths, "\n"), "ObjectType": kind}, &out)
	return taskIDs(out.RefreshTaskID), err
}

// PrefetchCDN has the CDN fetch URLs from the origin ahead of the first
// visitor, and returns the task IDs.
func (c *Client) PrefetchCDN(ctx context.Context, urls []string) ([]string, error) {
	if len(urls) == 0 {
		return nil, errors.New("没有要预热的地址")
	}
	var out struct {
		PushTaskID string `json:"PushTaskId"`
	}
	err := c.Call(ctx, ProductCDN, VersionCDN, "PushObjectCache", "", map[string]string{"ObjectPath": strings.Join(urls, "\n")}, &out)
	return taskIDs(out.PushTaskID), err
}

// CDNTask is one URL or folder of a purge or prefetch task.
type CDNTask struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Type     string `json:"type"`     // file, directory, preload, …
	Status   string `json:"status"`   // Complete, Refreshing, Failed, Timeout or Canceled
	Progress int    `json:"progress"` // percent
	Created  string `json:"created"`
	// Reason says why a task failed, e.g. Origin Timeout.
	Reason string `json:"reason,omitempty"`
}

// CDNTasks reads how purge and prefetch tasks are going.
func (c *Client) CDNTasks(ctx context.Context, ids ...string) ([]CDNTask, error) {
	var list []CDNTask
	for _, id := range ids {
		got := 0
		for page := 1; ; page++ {
			var out struct {
				Tasks struct {
					CDNTask []struct {
						TaskID       string `json:"TaskId"`
						ObjectPath   string `json:"ObjectPath"`
						ObjectType   string `json:"ObjectType"`
						Status       string `json:"Status"`
						Process      number `json:"Process"` // "100%"
						CreationTime string `json:"CreationTime"`
						Description  string `json:"Description"`
					} `json:"CDNTask"`
				} `json:"Tasks"`
				TotalCount int `json:"TotalCount"`
			}
			err := c.Call(ctx, ProductCDN, VersionCDN, "DescribeRefreshTasks", "",
				map[string]string{"TaskId": id, "PageNumber": strconv.Itoa(page), "PageSize": "100"}, &out)
			if err != nil {
				return list, err
			}
			for _, t := range out.Tasks.CDNTask {
				list = append(list, CDNTask{ID: t.TaskID, Path: t.ObjectPath, Type: t.ObjectType, Status: t.Status,
					Progress: int(t.Process), Created: rfc3339(t.CreationTime), Reason: t.Description})
			}
			got += len(out.Tasks.CDNTask)
			if len(out.Tasks.CDNTask) == 0 || got >= out.TotalCount {
				break
			}
		}
	}
	return list, nil
}

// SetCDNDomain turns a domain's acceleration on or off (StartCdnDomain,
// StopCdnDomain).
func (c *Client) SetCDNDomain(ctx context.Context, name string, on bool) error {
	action := "StopCdnDomain"
	if on {
		action = "StartCdnDomain"
	}
	return c.Call(ctx, ProductCDN, VersionCDN, action, "", map[string]string{"DomainName": name}, nil)
}
