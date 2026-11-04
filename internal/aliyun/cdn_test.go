package aliyun_test

import (
	"context"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
)

func TestCDNDomains(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	f.CDN = append(f.CDN, &aliyuntest.CDNDomain{Name: "img.example.com", Cname: "img.example.com.w.kunlunsl.com", Status: "offline", Origin: "oss.example.com"})
	f.PageSize = 1
	list, err := f.Client().CDNDomains(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("domains = %+v, %v", list, err)
	}
	d := list[0]
	if d.Name != "cdn.example.com" || d.CNAME != "cdn.example.com.w.kunlunsl.com" || d.Status != "online" || d.Type != "web" || d.HTTPS ||
		len(d.Sources) != 1 || d.Sources[0].Content != "47.96.1.2" || d.Sources[0].Port != 80 || d.Sources[0].Type != "ipaddr" || d.Created == "" {
		t.Fatalf("domain = %+v", d)
	}
	if list[1].Status != "offline" {
		t.Fatalf("second = %+v", list[1])
	}
}

func TestCDNPurgeAndPrefetch(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	f.CDN = append(f.CDN, &aliyuntest.CDNDomain{Name: "img.example.com", Status: "online"})
	c := f.Client()
	ctx := context.Background()
	ids, err := c.PurgeCDN(ctx, []string{"https://cdn.example.com/a.css", "https://cdn.example.com/b.js", "https://img.example.com/logo.png"}, false)
	if err != nil || len(ids) != 2 {
		t.Fatalf("purge: %v %v", ids, err)
	}
	f.PageSize = 1
	tasks, err := c.CDNTasks(ctx, ids...)
	if err != nil || len(tasks) != 3 || tasks[0].Path != "https://cdn.example.com/a.css" || tasks[0].Type != "file" ||
		tasks[0].Status != "Refreshing" || tasks[0].Progress != 0 || tasks[0].Created == "" {
		t.Fatalf("tasks = %+v, %v", tasks, err)
	}
	tasks, _ = c.CDNTasks(ctx, ids[0])
	if len(tasks) != 2 || tasks[0].Status != "Complete" || tasks[0].Progress != 100 {
		t.Fatalf("later = %+v", tasks)
	}
	if _, err := c.PurgeCDN(ctx, []string{"https://cdn.example.com/static"}, true); !aliyun.IsCode(err, "InvalidObjectPath") {
		t.Fatalf("folder without a slash: %v", err)
	}
	ids, err = c.PurgeCDN(ctx, []string{"https://cdn.example.com/static/"}, true)
	if err != nil || len(ids) != 1 {
		t.Fatalf("folder: %v %v", ids, err)
	}
	if tasks, _ := c.CDNTasks(ctx, ids...); len(tasks) != 1 || tasks[0].Type != "directory" {
		t.Fatalf("folder task = %+v", tasks)
	}
	ids, err = c.PrefetchCDN(ctx, []string{"https://cdn.example.com/big.zip"})
	if err != nil || len(ids) != 1 {
		t.Fatalf("prefetch: %v %v", ids, err)
	}
	if tasks, _ := c.CDNTasks(ctx, ids...); len(tasks) != 1 || tasks[0].Type != "preload" {
		t.Fatalf("prefetch task = %+v", tasks)
	}
	if _, err := c.PurgeCDN(ctx, []string{"https://elsewhere.org/x"}, false); err == nil {
		t.Fatal("purged a domain that is not on the CDN")
	}
	if _, err := c.PurgeCDN(ctx, nil, false); err == nil {
		t.Fatal("purged nothing")
	}
}
