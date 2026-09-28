package aliyun

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Metrics Miao Panel reads for both kinds of server; MonitorData maps
// them to each product's own. Any other name goes to the product as is
// (e.g. load_1m for ECS, DISKUSAGE_USED or FLOW_USED for Simple
// Application Server).
const (
	MetricCPU    = "cpu"     // percent
	MetricMemory = "memory"  // percent; ECS needs the CloudMonitor agent for it
	MetricNetIn  = "net_in"  // public inbound bandwidth, bit/s
	MetricNetOut = "net_out" // public outbound bandwidth, bit/s
)

// Point is one sample of a time series.
type Point struct {
	T int64   `json:"t"` // unix seconds
	V float64 `json:"v"`
}

// MonitorData reads one metric of a server between start and end, one
// point per period seconds (60 when zero). ECS data comes from
// CloudMonitor, Simple Application Server data from its own
// DescribeMonitorData, which takes a period of 60, 300 or 900 only.
func (c *Client) MonitorData(ctx context.Context, s Server, metric string, period int, start, end time.Time) ([]Point, error) {
	if period <= 0 {
		period = 60
	}
	if s.Kind == KindSWAS {
		name := map[string]string{MetricCPU: "CPU_UTILIZATION", MetricMemory: "MEMORY_ACTUALUSEDSPACE",
			MetricNetIn: "VPC_PUBLICIP_INTERNETIN_RATE", MetricNetOut: "VPC_PUBLICIP_INTERNETOUT_RATE"}[metric]
		if name == "" {
			name = metric
		}
		pts, err := c.swasMetric(ctx, s, name, period, start, end)
		// It reports memory in bytes; a percentage reads the same for both kinds.
		if metric == MetricMemory && s.MemoryGB > 0 {
			for i := range pts {
				pts[i].V = pts[i].V * 100 / (s.MemoryGB * (1 << 30))
			}
		}
		return pts, err
	}
	name := map[string]string{MetricCPU: "CPUUtilization", MetricMemory: "memory_usedutilization",
		MetricNetIn: "InternetInRate", MetricNetOut: "InternetOutRate"}[metric]
	if name == "" {
		name = metric
	}
	pts, err := c.CloudMonitor(ctx, s.Region, "acs_ecs_dashboard", name, map[string]string{"instanceId": s.ID}, period, start, end)
	// InternetInRate/OutRate cover the classic public IP; an instance in a
	// VPC reports its public traffic per IP instead.
	if err == nil && len(pts) == 0 && (metric == MetricNetIn || metric == MetricNetOut) && len(s.PublicIPs) > 0 {
		pts, err = c.CloudMonitor(ctx, s.Region, "acs_ecs_dashboard", "VPC_PublicIP_"+name,
			map[string]string{"instanceId": s.ID, "ip": s.PublicIPs[0]}, period, start, end)
	}
	return pts, err
}

// CloudMonitor reads a metric of one resource from CloudMonitor
// (DescribeMetricList), following every page.
func (c *Client) CloudMonitor(ctx context.Context, region, namespace, metric string, dims map[string]string, period int, start, end time.Time) ([]Point, error) {
	d, _ := json.Marshal([]map[string]string{dims})
	var pts []Point
	for token := ""; ; {
		in := map[string]string{"Namespace": namespace, "MetricName": metric, "Period": strconv.Itoa(period),
			"StartTime": strconv.FormatInt(start.UnixMilli(), 10), "EndTime": strconv.FormatInt(end.UnixMilli(), 10),
			"Dimensions": string(d), "Length": "1440"}
		if token != "" {
			in["NextToken"] = token
		}
		var out struct {
			Datapoints string `json:"Datapoints"`
			NextToken  string `json:"NextToken"`
		}
		if err := c.Call(ctx, ProductCMS, VersionCMS, "DescribeMetricList", region, in, &out); err != nil {
			return pts, err
		}
		pts = append(pts, datapoints(out.Datapoints)...)
		if out.NextToken == "" || out.NextToken == token {
			break
		}
		token = out.NextToken
	}
	sortPoints(pts)
	return pts, nil
}

func (c *Client) swasMetric(ctx context.Context, s Server, name string, period int, start, end time.Time) ([]Point, error) {
	switch {
	case name == "FLOW_USED":
		period = 3600
	case period <= 60:
		period = 60
	case period <= 300:
		period = 300
	default:
		period = 900
	}
	var pts []Point
	for token := ""; ; {
		in := map[string]string{"InstanceId": s.ID, "MetricName": name, "Period": strconv.Itoa(period),
			"StartTime": start.UTC().Format("2006-01-02T15:04:05Z"), "EndTime": end.UTC().Format("2006-01-02T15:04:05Z"), "Length": "1440"}
		if token != "" {
			in["NextToken"] = token
		}
		var out struct {
			Datapoints string `json:"Datapoints"`
			NextToken  string `json:"NextToken"`
		}
		if err := c.Call(ctx, ProductSWAS, VersionSWAS, "DescribeMonitorData", s.Region, in, &out); err != nil {
			return pts, err
		}
		pts = append(pts, datapoints(out.Datapoints)...)
		if out.NextToken == "" || out.NextToken == token {
			break
		}
		token = out.NextToken
	}
	sortPoints(pts)
	return pts, nil
}

// datapoints reads the JSON array both monitoring APIs return as a
// string, e.g. [{"timestamp":1548777660000,"Average":9.92,…}]. The
// timestamp is in milliseconds; the value is the average when there is
// one.
func datapoints(s string) []Point {
	var raw []map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &raw) != nil {
		return nil
	}
	pick := func(m map[string]json.RawMessage, names ...string) (json.RawMessage, bool) {
		for _, n := range names {
			for k, v := range m {
				if strings.EqualFold(k, n) && string(v) != "null" {
					return v, true
				}
			}
		}
		return nil, false
	}
	var pts []Point
	for _, m := range raw {
		rt, ok := pick(m, "timestamp", "time")
		if !ok {
			continue
		}
		var t number
		_ = json.Unmarshal(rt, &t)
		ts := int64(t)
		if ts > 1e11 { // milliseconds
			ts /= 1000
		}
		var iso string
		if json.Unmarshal(rt, &iso) == nil {
			if at, err := time.Parse(time.RFC3339, iso); err == nil {
				ts = at.Unix()
			}
		}
		rv, ok := pick(m, "Average", "Value", "Maximum", "Sum", "Minimum")
		if !ok || ts == 0 {
			continue
		}
		var v number
		_ = json.Unmarshal(rv, &v)
		pts = append(pts, Point{T: ts, V: float64(v)})
	}
	return pts
}

func sortPoints(pts []Point) {
	sort.SliceStable(pts, func(i, j int) bool { return pts[i].T < pts[j].T })
}
