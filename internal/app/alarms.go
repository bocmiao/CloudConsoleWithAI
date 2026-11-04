package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cloud monitoring alarms: what Tencent Cloud's 云监控 and 阿里云's 云监控
// raised in the last week, next to Miao Panel's own monitoring. The 监控
// page lists them, 总览 shows those still going on, an alert mentions new
// ones, and the AI reads them with cloud_alarms.

const alarmDays = 7

// CloudAlarm is one alarm of a cloud's monitoring.
type CloudAlarm struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`    // stays the same while it goes on
	Object   string `json:"object"` // the server or resource
	What     string `json:"what"`   // the condition, e.g. CPU利用率 > 90%
	Policy   string `json:"policy,omitempty"`
	Level    string `json:"level"` // crit, warn, info
	Active   bool   `json:"active"`
	First    string `json:"first"` // RFC 3339
	Last     string `json:"last"`
}

// CloudAlarmsView is the 监控 page's 云监控告警.
type CloudAlarmsView struct {
	Alarms []CloudAlarm `json:"alarms"`
	Errors []string     `json:"errors"`
	Days   int          `json:"days"`
}

// CloudAlarms reads both clouds' alarms of the last week, the ones going
// on first.
func (a *App) CloudAlarms(ctx context.Context) (CloudAlarmsView, error) {
	v := CloudAlarmsView{Alarms: []CloudAlarm{}, Errors: []string{}, Days: alarmDays}
	tc, ac := a.tencentClient(), a.aliyunClient()
	if tc == nil && ac == nil {
		return v, userErr("还没有配置腾讯云或阿里云的密钥（设置 → 腾讯云 / 阿里云）")
	}
	since := time.Now().Add(-alarmDays * 24 * time.Hour)
	var mu sync.Mutex
	var wg sync.WaitGroup
	if tc != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			list, err := tc.Alarms(ctx, since)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				v.Errors = append(v.Errors, "腾讯云云监控："+err.Error())
			}
			for _, x := range list {
				level := map[string]string{"Serious": "crit", "Warn": "warn", "Remind": "info"}[x.Level]
				if level == "" {
					level = "warn"
				}
				v.Alarms = append(v.Alarms, CloudAlarm{Provider: "tencent", Key: "tencent:" + x.ID, Object: x.Object, What: x.Content, Policy: x.Policy,
					Level: level, Active: x.Status == "ALARM", First: x.First, Last: x.Last})
			}
		}()
	}
	if ac != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			list, err := ac.Alarms(ctx, since)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				v.Errors = append(v.Errors, "阿里云云监控："+err.Error())
			}
			// The log has an entry each time it went off or recovered: one
			// alarm per rule and resource, as its latest entry says.
			// Going back from the newest entry, a recovery before the
			// episode's first trigger ends it.
			byKey := map[string]*CloudAlarm{}
			closed := map[string]bool{}
			var keys []string
			for _, x := range list { // newest first
				key := "aliyun:" + x.RuleID + ":" + x.Instance
				recovery := x.Level == "OK" || strings.HasSuffix(x.Change, "->OK")
				c := byKey[key]
				if c == nil {
					object := x.InstanceName
					if object == "" {
						object = x.Instance
					} else if x.Instance != "" {
						object += "（" + x.Instance + "）"
					}
					what := x.Expression
					if what == "" {
						what = x.Metric
					}
					c = &CloudAlarm{Provider: "aliyun", Key: key, Object: object, What: what, Policy: x.Rule, Level: aliLevel(x.Level, x.Change),
						Active: !recovery, Last: x.Time}
					byKey[key] = c
					keys = append(keys, key)
				}
				switch {
				case closed[key]:
				case recovery && c.First != "":
					closed[key] = true // an earlier episode
				case !recovery:
					c.First = x.Time
				}
			}
			for _, k := range keys {
				c := byKey[k]
				if c.First == "" {
					c.First = c.Last // it went off before the week began
				}
				c.Key += ":" + c.First
				v.Alarms = append(v.Alarms, *c)
			}
		}()
	}
	wg.Wait()
	rank := map[string]int{"crit": 0, "warn": 1, "info": 2}
	sort.SliceStable(v.Alarms, func(i, j int) bool {
		x, y := v.Alarms[i], v.Alarms[j]
		if x.Active != y.Active {
			return x.Active
		}
		if x.Active && rank[x.Level] != rank[y.Level] {
			return rank[x.Level] < rank[y.Level]
		}
		return x.Last > y.Last
	})
	sort.Strings(v.Errors)
	return v, nil
}

// aliLevel reads 阿里云's alert levels: P2 (phone) is the most urgent; a
// recovery keeps the level it recovered from.
func aliLevel(level, change string) string {
	if level == "OK" {
		level, _, _ = strings.Cut(change, "->")
	}
	switch level {
	case "P1", "P2", "CRITICAL":
		return "crit"
	case "P4", "INFO":
		return "info"
	}
	return "warn"
}

// CloudAlarmsPage is 云监控告警, kept ready.
func (a *App) CloudAlarmsPage(ctx context.Context, read PageRead) (CloudAlarmsView, PageMeta, error) {
	return page(ctx, a, "page_alarms", read, a.CloudAlarms)
}

// alarmTodo is what the clouds' monitoring raises now, for 总览.
func alarmTodo(v CloudAlarmsView) []OverviewItem {
	var out []OverviewItem
	for _, x := range v.Alarms {
		if !x.Active || x.Level == "info" {
			continue
		}
		out = append(out, OverviewItem{Level: x.Level, Kind: "alarm", Action: "去看看",
			Title: fmt.Sprintf("云监控告警：%s", x.Object), Meta: providerName[x.Provider] + " · " + x.What + " · " + whenShort(x.First) + "开始"})
	}
	return out
}

// toolCloudAlarms is the AI's look at the clouds' monitoring alarms.
func (a *App) toolCloudAlarms(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Refresh bool `json:"refresh"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	if !a.hasCloud() {
		return "还没有配置腾讯云或阿里云的密钥。", nil
	}
	read := PageWait
	if arg.Refresh {
		read = PageRefresh
	}
	v, _, err := a.CloudAlarmsPage(ctx, read)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "腾讯云和阿里云云监控最近 %d 天的告警（没有恢复的在前）：\n", v.Days)
	if len(v.Alarms) == 0 {
		b.WriteString("- 没有（也可能是还没有在云监控里配置告警策略）\n")
	}
	levels := map[string]string{"crit": "严重", "warn": "警告", "info": "提醒"}
	for i, x := range v.Alarms {
		if i >= 50 {
			fmt.Fprintf(&b, "- 还有 %d 条\n", len(v.Alarms)-50)
			break
		}
		state := "已恢复"
		if x.Active {
			state = "未恢复"
		}
		fmt.Fprintf(&b, "- [%s %s %s] %s：%s", providerName[x.Provider], levels[x.Level], state, x.Object, x.What)
		if x.Policy != "" {
			fmt.Fprintf(&b, "（策略 %s）", x.Policy)
		}
		fmt.Fprintf(&b, " 开始 %s，最近 %s\n", x.First, x.Last)
	}
	for _, e := range v.Errors {
		fmt.Fprintf(&b, "读取失败：%s\n", e)
	}
	return b.String(), nil
}
