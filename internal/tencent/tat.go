package tencent

import (
	"context"
	"encoding/base64"
)

const tatVersion = "2020-10-28"

// AgentStatus reports whether an instance's automation agent (TAT) is
// Online or Offline; "" means it is not installed.
func (c *Client) AgentStatus(ctx context.Context, region, instanceID string) (string, error) {
	var out struct {
		AutomationAgentSet []struct {
			InstanceID  string `json:"InstanceId"`
			AgentStatus string `json:"AgentStatus"`
		} `json:"AutomationAgentSet"`
	}
	err := c.CallRegion(ctx, "tat", tatVersion, "DescribeAutomationAgentStatus", region,
		map[string]any{"InstanceIds": []string{instanceID}}, &out)
	for _, a := range out.AutomationAgentSet {
		if a.InstanceID == instanceID {
			return a.AgentStatus, err
		}
	}
	return "", err
}

// RunShell starts a shell script as root on an instance and returns the
// invocation to poll. timeout is in seconds.
func (c *Client) RunShell(ctx context.Context, region, instanceID, script string, timeout int) (string, error) {
	var out struct {
		InvocationID string `json:"InvocationId"`
	}
	err := c.CallRegion(ctx, "tat", tatVersion, "RunCommand", region, map[string]any{
		"Content": base64.StdEncoding.EncodeToString([]byte(script)), "InstanceIds": []string{instanceID},
		"CommandType": "SHELL", "CommandName": "MiaoPanel", "Timeout": timeout, "Username": "root",
		"WorkingDirectory": "/root", "SaveCommand": false,
	}, &out)
	return out.InvocationID, err
}

// Task is how a command is doing on the instance.
type Task struct {
	Status    string // PENDING, DELIVERING, RUNNING, SUCCESS, FAILED, TIMEOUT, ...
	ExitCode  int
	Output    string // at most 24KB, already decoded
	Dropped   int    // output bytes cut off
	ErrorInfo string
}

// Finished reports whether the task will not change any more.
func (t Task) Finished() bool {
	switch t.Status {
	case "", "PENDING", "DELIVERING", "DELIVER_DELAYED", "RUNNING", "CANCELLING":
		return false
	}
	return true
}

// Invocation reads the result of a command started with RunShell.
func (c *Client) Invocation(ctx context.Context, region, invocationID string) (Task, error) {
	var out struct {
		InvocationTaskSet []struct {
			TaskStatus string `json:"TaskStatus"`
			ErrorInfo  string `json:"ErrorInfo"`
			TaskResult *struct {
				ExitCode int    `json:"ExitCode"`
				Output   string `json:"Output"`
				Dropped  int    `json:"Dropped"`
			} `json:"TaskResult"`
		} `json:"InvocationTaskSet"`
	}
	err := c.CallRegion(ctx, "tat", tatVersion, "DescribeInvocationTasks", region, map[string]any{
		"Filters":    []map[string]any{{"Name": "invocation-id", "Values": []string{invocationID}}},
		"HideOutput": false,
	}, &out)
	if err != nil || len(out.InvocationTaskSet) == 0 {
		return Task{}, err
	}
	t := out.InvocationTaskSet[0]
	task := Task{Status: t.TaskStatus, ErrorInfo: t.ErrorInfo}
	if r := t.TaskResult; r != nil {
		task.ExitCode, task.Dropped = r.ExitCode, r.Dropped
		raw, err := base64.StdEncoding.DecodeString(r.Output)
		if err != nil {
			return task, err
		}
		task.Output = string(raw)
	}
	return task, nil
}
