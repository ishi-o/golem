package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// The built-in tool names. Constants rather than string literals because the
// ScheduledTask scenario excludes the schedule tools by name, and an
// integration filtering by a name it mis-spelled would silently filter
// nothing.
const (
	ToolNameCurrentDateTime = "CurrentDateTime"

	ToolNameReadFile  = "ReadFile"
	ToolNameWriteFile = "WriteFile"
	ToolNameListFiles = "ListFiles"
	ToolNameGrepFiles = "GrepFiles"

	ToolNameReadMemory  = "MemoryView"
	ToolNameWriteMemory = "MemoryWrite"

	ToolNameListSkills    = "ListSkills"
	ToolNameReadSkillFile = "ReadSkillFile"
	ToolNameWriteSkill    = "WriteSkillFile"
	ToolNameDeleteSkill   = "DeleteSkill"

	ToolNameTodoWrite = "TodoWrite"

	ToolNameBash                  = "Bash"
	ToolNameBashOutput            = "BashOutput"
	ToolNameKillShell             = "KillShell"
	ToolNameRestartShellContainer = "RestartShellContainer"
	ToolNameRestartShellPod       = "RestartShellPod"
	ToolNameRestartSandbox        = "RestartSandbox"

	ToolNameSetCredential    = "SetCredential"
	ToolNameListCredentials  = "ListCredentials"
	ToolNameDeleteCredential = "DeleteCredential"

	ToolNamePublishFile         = "PublishFile"
	ToolNameUpdatePublishedFile = "UpdatePublishedFile"
	ToolNameUnpublishFile       = "UnpublishFile"
	ToolNameRenewPublishedFile  = "RenewPublishedFile"

	ToolNameAskUserQuestion = "AskUserQuestionTool"

	ToolNameCreateScheduledTask = "CreateScheduledTask"
	ToolNameListScheduledTasks  = "ListScheduledTasks"
	ToolNameCancelScheduledTask = "CancelScheduledTask"

	ToolNameToolSearch = "tool_search"
)

// MustTool builds a tool whose schema is inferred from a static Go struct —
// a build-time-shaped operation done at init time. A failure here is a
// programming error in the caller (an uninferable type), and failing the
// process at first use is the honest outcome; there is nothing a runtime
// caller could do about it later.
func MustTool(t tool.InvokableTool, err error) tool.InvokableTool {
	if err != nil {
		panic(fmt.Sprintf("golem/tools: uninferable tool schema: %v", err))
	}
	return t
}

// mustTool is MustTool for this package's own built-ins.
func mustTool(t tool.InvokableTool, err error) tool.InvokableTool {
	return MustTool(t, err)
}

// CurrentDateTime reports the wall clock. The default system prompt sends
// the model here whenever an answer depends on "today" or "in two hours": a
// model's sense of now is its training cutoff, which is never now.
func CurrentDateTime() tool.InvokableTool {
	type output struct {
		DateTime string `json:"dateTime"`
	}
	return mustTool(utils.InferTool(ToolNameCurrentDateTime,
		"Get the current date and time, with timezone. Call this whenever the answer depends on the current date or time, including relative expressions like today, this week or in two hours.",
		func(ctx context.Context, _ struct{}) (output, error) {
			return output{DateTime: time.Now().Format(time.RFC3339)}, nil
		}))
}
