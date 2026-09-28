package aliyun

import (
	"encoding/json"
	"testing"
)

// The monitoring APIs return their samples as a JSON string whose exact
// fields Simple Application Server does not document; read the usual
// spellings.
func TestDatapoints(t *testing.T) {
	pts := datapoints(`[{"timestamp":1548777660000,"instanceId":"i-abc","Minimum":1,"Average":9.92,"Maximum":20},
		{"timestamp":"1548777720","Value":"3.5"},
		{"time":"2019-01-29T16:03:00Z","average":7},
		{"timestamp":1548777840000,"Average":null,"Maximum":4},
		{"timestamp":1548777900000},
		{"Average":1}]`)
	want := []Point{{1548777660, 9.92}, {1548777720, 3.5}, {1548777780, 7}, {1548777840, 4}}
	if len(pts) != len(want) {
		t.Fatalf("points = %+v", pts)
	}
	for i := range want {
		if pts[i] != want[i] {
			t.Errorf("point %d = %+v, want %+v", i, pts[i], want[i])
		}
	}
	if datapoints("") != nil || datapoints("[]") != nil {
		t.Error("empty data gives points")
	}
}

func TestPorts(t *testing.T) {
	for api, port := range map[string]string{"22/22": "22", "1/200": "1-200", "-1/-1": "", "3306": "3306", "1024/1055": "1024-1055", "": ""} {
		if got := ports(api); got != port {
			t.Errorf("ports(%q) = %q, want %q", api, got, port)
		}
	}
	for _, c := range []struct {
		port     string
		ecs      bool
		api      string
		fromBack string
	}{
		{"22", true, "22/22", "22"}, {"22", false, "22", "22"}, {"8000-9000", true, "8000/9000", "8000-9000"},
		{"8000/9000", false, "8000/9000", "8000-9000"}, {"", true, "-1/-1", ""}, {"ALL", false, "-1/-1", ""},
	} {
		if got := apiPorts(c.port, c.ecs); got != c.api || ports(got) != c.fromBack {
			t.Errorf("apiPorts(%q, %v) = %q", c.port, c.ecs, got)
		}
	}
}

func TestLooseJSON(t *testing.T) {
	var v struct {
		A, B, C, D text
		N, M, P    number
	}
	if err := json.Unmarshal([]byte(`{"A":"x","B":200,"C":false,"D":null,"N":"12","M":3.5,"P":"100%"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != "x" || v.B != "200" || v.C != "false" || v.D != "" || v.N != 12 || v.M != 3.5 || v.P != 100 {
		t.Fatalf("%+v", v)
	}
	for in, want := range map[string]string{"2017-12-10T04:04Z": "2017-12-10T04:04:00Z", "2021-05-08T16:00:00Z": "2021-05-08T16:00:00Z", "soon": "soon"} {
		if got := rfc3339(in); got != want {
			t.Errorf("rfc3339(%q) = %q", in, got)
		}
	}
	if expiry("2099-12-31T15:59Z") != "" {
		t.Error("pay-as-you-go instances expire")
	}
}
