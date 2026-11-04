package tencent_test

import (
	"context"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func TestAccount(t *testing.T) {
	f := tencenttest.Start(t)
	f.BalanceFen, f.OweFen = 12345, 500
	f.Registered = []tencent.RegisteredDomain{{Name: "Example.COM", Expires: "2027-03-01", AutoRenew: true}, {Name: "blog.cn", Expires: "2026-10-20"}}
	c, ctx := f.Client(), context.Background()

	b, err := c.Balance(ctx)
	if err != nil || b.Available != 123.45 || b.Owed != 5 {
		t.Fatalf("balance = %+v %v", b, err)
	}
	ds, err := c.RegisteredDomains(ctx)
	if err != nil || len(ds) != 2 || ds[0].Name != "example.com" || ds[0].Expires != "2027-03-01" || !ds[0].AutoRenew || ds[1].AutoRenew {
		t.Fatalf("domains = %+v %v", ds, err)
	}

	for _, id := range []string{"lhins-abc12345", "ins-abc12345"} {
		if f.Instances[id] == nil {
			continue
		}
		if err := c.SetRenewFlag(ctx, "ap-guangzhou", id, tencent.RenewAuto); err != nil {
			t.Fatal(err)
		}
		if f.Instances[id].RenewFlag != tencent.RenewAuto {
			t.Errorf("%s renew flag = %q", id, f.Instances[id].RenewFlag)
		}
	}
	if err := c.SetRenewFlag(ctx, "ap-guangzhou", "lhins-abc12345", "SOMETIMES"); err == nil {
		t.Error("a wrong flag was taken")
	}
}
