package aliyun_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
)

func TestAccount(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	f.Balance = 1234.5
	for i := range 130 {
		f.Registered = append(f.Registered, aliyun.RegisteredDomain{Name: fmt.Sprintf("Site%d.com", i), Expires: "2027-01-02"})
	}
	c, ctx := f.Client(), context.Background()
	b, err := c.Balance(ctx)
	if err != nil || b.Available != 1234.5 || b.Currency != "CNY" {
		t.Fatalf("balance = %+v %v", b, err)
	}
	ds, err := c.RegisteredDomains(ctx)
	if err != nil || len(ds) != 130 || ds[0].Name != "site0.com" || ds[0].Expires != "2027-01-02" {
		t.Fatalf("domains = %d %+v %v", len(ds), ds[:1], err)
	}

	var ecs *aliyuntest.ECSInstance
	for _, i := range f.ECS {
		if i.ChargeType == "PrePaid" {
			ecs = i
		}
	}
	if ecs == nil {
		t.Fatal("the fake has no subscription ECS")
	}
	for _, on := range []bool{false, true, false} {
		if err := c.SetAutoRenew(ctx, ecs.Region, ecs.ID, on); err != nil {
			t.Fatal(err)
		}
		if ecs.AutoRenew != on {
			t.Errorf("auto renew = %v, want %v", ecs.AutoRenew, on)
		}
	}
	for id := range f.SWAS {
		if err := c.SetAutoRenew(ctx, "cn-shanghai", id, true); err == nil {
			t.Error("a Simple Application Server was set to renew")
		}
		break
	}
}
