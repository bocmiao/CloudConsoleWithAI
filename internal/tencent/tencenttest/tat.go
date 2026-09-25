package tencenttest

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"time"
)

type invocation struct {
	instance string
	output   string
	exitCode int
	looked   bool // the first look shows it still running
}

// tatOutputLimit is how much output TAT keeps.
const tatOutputLimit = 24 << 10

// localShell runs a script with the local sh, the way sshtest runs SSH
// commands, so scripts are tested end to end.
func localShell(_ string, script string) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", script).CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode()
	}
	return string(out), 0
}

// serveTAT handles the automation agent (TAT) calls.
func (f *Fake) serveTAT(w http.ResponseWriter, service, action, region string, in map[string]any) bool {
	str := func(k string) string { s, _ := in[k].(string); return s }
	first := func(k string) string {
		list, _ := in[k].([]any)
		if len(list) == 0 {
			return ""
		}
		s, _ := list[0].(string)
		return s
	}
	switch service + " " + action {
	case "tat DescribeAutomationAgentStatus":
		id := first("InstanceIds")
		var set []map[string]string
		if f.Instances[id] != nil && region == "ap-guangzhou" {
			status := "Offline"
			if f.AgentOnline[id] {
				status = "Online"
			}
			set = append(set, map[string]string{"InstanceId": id, "AgentStatus": status, "Environment": "Linux"})
		}
		ok(w, map[string]any{"AutomationAgentSet": set, "TotalCount": len(set)})
	case "tat RunCommand":
		id := first("InstanceIds")
		switch {
		case f.Instances[id] == nil || region != "ap-guangzhou":
			fail(w, "InvalidParameterValue.InvalidInstanceId", "实例不存在。")
			return true
		case !f.AgentOnline[id]:
			fail(w, "ResourceUnavailable.AgentStatusNotOnline", "Agent 不在线。")
			return true
		case str("CommandType") != "SHELL" || str("Username") != "root":
			fail(w, "InvalidParameter", "fake expects SHELL commands as root")
			return true
		}
		raw, err := base64.StdEncoding.DecodeString(str("Content"))
		if err != nil || len(str("Content")) > 64<<10 {
			fail(w, "InvalidParameterValue.CommandContentInvalid", "命令内容无效。")
			return true
		}
		run := f.RunShell
		if run == nil {
			run = localShell
		}
		out, code := run(id, string(raw))
		f.nextID++
		inv := fmt.Sprintf("inv-%d", f.nextID)
		f.invocations[inv] = &invocation{instance: id, output: out, exitCode: code}
		ok(w, map[string]any{"CommandId": "cmd-1", "InvocationId": inv})
	case "tat DescribeInvocationTasks":
		var id string
		if fl, _ := in["Filters"].([]any); len(fl) > 0 {
			if v, _ := fl[0].(map[string]any)["Values"].([]any); len(v) > 0 {
				id, _ = v[0].(string)
			}
		}
		inv := f.invocations[id]
		if inv == nil {
			ok(w, map[string]any{"InvocationTaskSet": []any{}, "TotalCount": 0})
			return true
		}
		task := map[string]any{"InvocationId": id, "InvocationTaskId": id + "-t", "InstanceId": inv.instance, "TaskStatus": "RUNNING"}
		if inv.looked {
			out, dropped := inv.output, 0
			if len(out) > tatOutputLimit {
				out, dropped = out[:tatOutputLimit], len(out)-tatOutputLimit
			}
			if in["HideOutput"] != false {
				out = ""
			}
			status := "SUCCESS"
			if inv.exitCode != 0 {
				status = "FAILED"
			}
			task["TaskStatus"] = status
			task["TaskResult"] = map[string]any{"ExitCode": inv.exitCode, "Output": base64.StdEncoding.EncodeToString([]byte(out)), "Dropped": dropped}
		}
		inv.looked = true
		ok(w, map[string]any{"InvocationTaskSet": []any{task}, "TotalCount": 1})
	default:
		return false
	}
	return true
}
