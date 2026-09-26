package tencent

import (
	"context"
	"time"
)

// Filter narrows EdgeOne analytics, e.g. {Key: "domain", Operator:
// "equals", Value: ["blog.example.com"]}.
type Filter struct {
	Key      string   `json:"Key"`
	Operator string   `json:"Operator"`
	Value    []string `json:"Value"`
}

// Series is one metric over time.
type Series struct {
	Metric string  `json:"metric"`
	Sum    int64   `json:"sum"`
	Max    int64   `json:"max"`
	Avg    int64   `json:"avg"`
	Points []Point `json:"points"`
}

// cst is the time zone EdgeOne analytics are asked in.
var cst = time.FixedZone("CST", 8*3600)

// TimeArg formats a time the way the analytics APIs expect.
func TimeArg(t time.Time) string { return t.In(cst).Format(time.RFC3339) }

type timingResp struct {
	Data []struct {
		TypeValue []struct {
			MetricName string `json:"MetricName"`
			Sum        int64  `json:"Sum"`
			Max        int64  `json:"Max"`
			Avg        int64  `json:"Avg"`
			Detail     []struct {
				Timestamp int64 `json:"Timestamp"`
				Value     int64 `json:"Value"`
			} `json:"Detail"`
		} `json:"TypeValue"`
	} `json:"Data"`
}

func (r timingResp) series() []Series {
	var out []Series
	for _, rec := range r.Data {
		for _, tv := range rec.TypeValue {
			s := Series{Metric: tv.MetricName, Sum: tv.Sum, Max: tv.Max, Avg: tv.Avg}
			for _, d := range tv.Detail {
				s.Points = append(s.Points, Point{T: d.Timestamp, V: float64(d.Value)})
			}
			out = append(out, s)
		}
	}
	return out
}

func timingArgs(zones, metrics []string, start, end time.Time, interval string, filters []Filter) map[string]any {
	in := map[string]any{"ZoneIds": zones, "MetricNames": metrics, "StartTime": TimeArg(start), "EndTime": TimeArg(end)}
	if interval != "" {
		in["Interval"] = interval
	}
	if len(filters) > 0 {
		in["Filters"] = filters
	}
	return in
}

// L7Timing reads access metrics over time: l7Flow_request, l7Flow_outFlux,
// l7Flow_outBandwidth, l7Flow_avgResponseTime, ...
func (c *Client) L7Timing(ctx context.Context, zones, metrics []string, start, end time.Time, interval string, filters []Filter) ([]Series, error) {
	var out timingResp
	err := c.Call(ctx, "teo", teoVersion, "DescribeTimingL7AnalysisData", timingArgs(zones, metrics, start, end, interval, filters), &out)
	return out.series(), err
}

// L7CacheTiming reads cache metrics (l7Cache_outFlux, l7Cache_request)
// over time; filter by cacheType hit/miss/dynamic for the hit ratio.
func (c *Client) L7CacheTiming(ctx context.Context, zones, metrics []string, start, end time.Time, interval string, filters []Filter) ([]Series, error) {
	var out timingResp
	err := c.Call(ctx, "teo", teoVersion, "DescribeTimingL7CacheData", timingArgs(zones, metrics, start, end, interval, filters), &out)
	return out.series(), err
}

// TopItem is one row of a ranking.
type TopItem struct {
	Key   string `json:"key"`
	Value int64  `json:"value"`
}

// L7Top ranks access by a dimension, e.g. l7Flow_request_url,
// l7Flow_outFlux_sip (client IP), l7Flow_request_country.
func (c *Client) L7Top(ctx context.Context, zones []string, metric string, start, end time.Time, limit int, filters []Filter) ([]TopItem, error) {
	in := map[string]any{"ZoneIds": zones, "MetricName": metric, "StartTime": TimeArg(start), "EndTime": TimeArg(end), "Limit": limit}
	if len(filters) > 0 {
		in["Filters"] = filters
	}
	var out struct {
		Data []struct {
			DetailData []TopItem `json:"DetailData"`
		} `json:"Data"`
	}
	err := c.Call(ctx, "teo", teoVersion, "DescribeTopL7AnalysisData", in, &out)
	var items []TopItem
	for _, d := range out.Data {
		items = append(items, d.DetailData...)
	}
	return items, err
}

// Purge clears EdgeOne's cache: kind is purge_url, purge_prefix,
// purge_host or purge_all; method invalidate or delete.
func (c *Client) Purge(ctx context.Context, zoneID, kind, method string, targets []string) (string, []string, error) {
	in := map[string]any{"ZoneId": zoneID, "Type": kind}
	if kind != "purge_url" {
		in["Method"] = method
	}
	if len(targets) > 0 {
		in["Targets"] = targets
	}
	var out struct {
		JobID      string `json:"JobId"`
		FailedList []struct {
			Reason  string   `json:"Reason"`
			Targets []string `json:"Targets"`
		} `json:"FailedList"`
	}
	err := c.Call(ctx, "teo", teoVersion, "CreatePurgeTask", in, &out)
	var failed []string
	for _, f := range out.FailedList {
		for _, t := range f.Targets {
			failed = append(failed, t+"："+f.Reason)
		}
	}
	return out.JobID, failed, err
}

// Prefetch warms EdgeOne's cache with the given URLs.
func (c *Client) Prefetch(ctx context.Context, zoneID string, targets []string) (string, error) {
	var out struct {
		JobID string `json:"JobId"`
	}
	err := c.Call(ctx, "teo", teoVersion, "CreatePrefetchTask", map[string]any{"ZoneId": zoneID, "Targets": targets}, &out)
	return out.JobID, err
}

// SetOrigin changes where an acceleration domain fetches content from.
// hostHeader is the Host sent to the origin when it was set to a custom
// one; OriginInfo is replaced as a whole, so it is sent again.
func (c *Client) SetOrigin(ctx context.Context, zoneID, name, origin, protocol string, httpPort, httpsPort int, hostHeader string) error {
	info := map[string]any{"OriginType": "IP_DOMAIN", "Origin": origin}
	if hostHeader != "" {
		info["HostHeader"] = hostHeader
	}
	in := map[string]any{"ZoneId": zoneID, "DomainName": name, "OriginInfo": info}
	if protocol != "" {
		in["OriginProtocol"] = protocol
	}
	if httpPort > 0 {
		in["HttpOriginPort"] = httpPort
	}
	if httpsPort > 0 {
		in["HttpsOriginPort"] = httpsPort
	}
	return c.Call(ctx, "teo", teoVersion, "ModifyAccelerationDomain", in, nil)
}
