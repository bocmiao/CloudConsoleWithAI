package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Tool is a ToolDef plus the code that runs it.
type Tool struct {
	Def ToolDef
	Run func(ctx context.Context, args json.RawMessage) (string, error)
}

// Step records one tool call so the user can see what the AI looked at.
type Step struct {
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// Reply is the result of one user message.
type Reply struct {
	Text  string `json:"text"`
	Steps []Step `json:"steps"`
	Usage Usage  `json:"usage"`
}

// Agent runs the model/tool loop for one conversation.
type Agent struct {
	Session   Session
	Tools     map[string]Tool
	MaxRounds int
	// ToolTimeout bounds each tool call.
	ToolTimeout time.Duration
	// MaxToolOutput caps what one tool call feeds back to the model (runes).
	MaxToolOutput int
}

func truncate(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "\n…（输出过长，已截断）"
}

// Event reports what Ask is doing, for showing the answer as it comes.
type Event struct {
	// Type is one of:
	//   text      more of the answer (Text)
	//   thinking  more of the model's thinking (Text)
	//   reset     forget the answer text of this round so far
	//   round     the model is asked again after tools ran; text shown so
	//             far was a remark on the way, not the answer
	//   prepare   the model is writing a call to Tool
	//   tool      Tool is running with Args
	//   tool_done Tool finished (Error set if it failed)
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Tool  string `json:"tool,omitempty"`
	Args  string `json:"args,omitempty"`
	Error string `json:"error,omitempty"`
}

// interruptedNote stands in for an answer that did not finish, so the
// history still alternates between question and answer.
const interruptedNote = "（这次回答被中断了）"

// Ask sends one user message and runs tools until the model answers.
// With on, the answer is streamed and on sees the events above. If it
// fails or is cancelled, Reply.Text has what was said so far.
func (a *Agent) Ask(ctx context.Context, text string, on func(Event)) (Reply, error) {
	maxRounds := a.MaxRounds
	if maxRounds == 0 {
		maxRounds = 10
	}
	timeout := a.ToolTimeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	emit := func(Event) {}
	var onDelta func(Delta)
	var partial strings.Builder // this round's text so far
	if on != nil {
		emit = on
		onDelta = func(d Delta) {
			switch {
			case d.Reset:
				partial.Reset()
				emit(Event{Type: "reset"})
			case d.Text != "":
				partial.WriteString(d.Text)
				emit(Event{Type: "text", Text: d.Text})
			case d.Reasoning != "":
				emit(Event{Type: "thinking", Text: d.Reasoning})
			case d.Tool != "":
				emit(Event{Type: "prepare", Tool: d.Tool})
			}
		}
	}
	var reply Reply
	a.Session.AddUser(text)
	for round := 0; ; round++ {
		if round > 0 {
			emit(Event{Type: "round"})
		}
		partial.Reset()
		turn, err := a.Session.Next(ctx, onDelta)
		if err != nil {
			reply.Text = partial.String()
			// Keep question and answer alternating for the next question.
			a.Session.AddAssistant(strings.TrimSpace(reply.Text + "\n" + interruptedNote))
			return reply, err
		}
		reply.Usage.Add(turn.Usage)
		reply.Text = turn.Text
		if len(turn.ToolCalls) == 0 {
			return reply, nil
		}
		results := make([]ToolResult, 0, len(turn.ToolCalls))
		if round+1 >= maxRounds {
			// Every tool_use needs a result before the next user turn.
			for _, c := range turn.ToolCalls {
				results = append(results, ToolResult{CallID: c.ID, Content: "本轮查询次数已用完", IsError: true})
			}
			a.Session.AddToolResults(results)
			if reply.Text == "" {
				reply.Text = "这个问题需要查的内容比较多，本轮已达到查询上限。可以把问题拆小一点再问。"
			}
			return reply, nil
		}
		for _, c := range turn.ToolCalls {
			step := Step{Tool: c.Name, Args: string(c.Args)}
			res := ToolResult{CallID: c.ID}
			emit(Event{Type: "tool", Tool: c.Name, Args: step.Args})
			tool, ok := a.Tools[c.Name]
			if !ok {
				res.Content, res.IsError = fmt.Sprintf("没有名为 %s 的工具", c.Name), true
			} else {
				tctx, cancel := context.WithTimeout(ctx, timeout)
				out, err := tool.Run(tctx, c.Args)
				cancel()
				if err != nil {
					res.Content, res.IsError = err.Error(), true
				} else {
					res.Content = truncate(out, a.MaxToolOutput)
				}
			}
			if res.IsError {
				step.Error = res.Content
			} else {
				step.Output = truncate(res.Content, 2000)
			}
			emit(Event{Type: "tool_done", Tool: c.Name, Args: step.Args, Error: step.Error})
			reply.Steps = append(reply.Steps, step)
			results = append(results, res)
		}
		a.Session.AddToolResults(results)
	}
}
