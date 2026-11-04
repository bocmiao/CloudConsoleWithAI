package tencent

import (
	"context"
	"time"
)

// Alarm is one alarm of Cloud Monitor (云监控) from its history.
type Alarm struct {
	ID        string `json:"id"`
	Object    string `json:"object"`  // what alarmed, e.g. "web-1 (10.0.0.3)"
	Content   string `json:"content"` // e.g. "CPU利用率 > 90%"
	Policy    string `json:"policy"`
	Status    string `json:"status"` // ALARM (going on), OK, NO_CONF, NO_DATA
	Level     string `json:"level"`  // Remind, Warn, Serious, or empty
	Region    string `json:"region"`
	Namespace string `json:"namespace"`
	Metric    string `json:"metric"`
	First     string `json:"first"` // RFC 3339
	Last      string `json:"last"`
}

// Alarms reads Cloud Monitor's alarm history since a time, newest first
// (DescribeAlarmHistories, at most 500).
func (c *Client) Alarms(ctx context.Context, since time.Time) ([]Alarm, error) {
	var all []Alarm
	for page := 1; page <= 5; page++ {
		var out struct {
			TotalCount int `json:"TotalCount"`
			Histories  []struct {
				AlarmID        string `json:"AlarmId"`
				AlarmObject    string `json:"AlarmObject"`
				Content        string `json:"Content"`
				PolicyName     string `json:"PolicyName"`
				AlarmStatus    string `json:"AlarmStatus"`
				AlarmLevel     string `json:"AlarmLevel"`
				Region         string `json:"Region"`
				Namespace      string `json:"Namespace"`
				MetricName     string `json:"MetricName"`
				FirstOccurTime int64  `json:"FirstOccurTime"`
				LastOccurTime  int64  `json:"LastOccurTime"`
			} `json:"Histories"`
		}
		err := c.CallRegion(ctx, "monitor", monitorVersion, "DescribeAlarmHistories", "ap-guangzhou", map[string]any{
			"Module": "monitor", "PageNumber": page, "PageSize": 100, "Order": "DESC",
			"StartTime": since.Unix(), "EndTime": time.Now().Unix(),
		}, &out)
		if err != nil {
			return all, err
		}
		for _, h := range out.Histories {
			all = append(all, Alarm{ID: h.AlarmID, Object: h.AlarmObject, Content: h.Content, Policy: h.PolicyName, Status: h.AlarmStatus,
				Level: h.AlarmLevel, Region: h.Region, Namespace: h.Namespace, Metric: h.MetricName,
				First: unixTime(h.FirstOccurTime), Last: unixTime(h.LastOccurTime)})
		}
		if len(out.Histories) < 100 || len(all) >= out.TotalCount {
			break
		}
	}
	return all, nil
}

func unixTime(s int64) string {
	if s <= 0 {
		return ""
	}
	return time.Unix(s, 0).UTC().Format(time.RFC3339)
}
