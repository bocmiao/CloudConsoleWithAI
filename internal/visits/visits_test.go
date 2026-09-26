package visits

import "testing"

func TestParse(t *testing.T) {
	r := Parse("N\t2026-09-26\t3\t+0800\nE\t没有找到网站访问日志\n")
	if r.Problem == "" || len(r.Sites) != 0 || r.Today != "2026-09-26" || r.Days != 3 {
		t.Fatalf("no logs: %+v", r)
	}
	r = Parse("N\t2026-03-01\t3\t+0800\n" +
		"D\t*\t2026-02-28\t5\t2\t1\t1\t0\t100\t0\t0\n" +
		"S\t*\t5\t2\t1\t1\t0\t100\t0\t0\n" +
		"H\t*\t09\t5\t2\n" +
		"T\t*\tpage\t1\ta.example.com/b\n" +
		"T\t*\tpage\t2\ta.example.com/a\n" +
		"broken line\n")
	all, ok := r.Site(All)
	if !ok || len(all.Days) != 3 || all.Days[0].Date != "2026-02-27" || all.Days[1].PV != 2 || all.Days[2].Requests != 0 {
		t.Fatalf("days = %+v", all.Days)
	}
	if len(all.Hours) != 24 || all.Hours[9].Requests != 5 || all.Top["page"][0].Value != "a.example.com/a" {
		t.Fatalf("hours/top = %+v %+v", all.Hours[9], all.Top)
	}
}
