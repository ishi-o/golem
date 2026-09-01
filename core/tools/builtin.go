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

	ToolNameReadMemory    = "MemoryView"
	ToolNameWriteMemory   = "MemoryWrite"
	ToolNameCreateMemory  = "MemoryCreate"
	ToolNameInsertMemory  = "MemoryInsert"
	ToolNameReplaceMemory = "MemoryStrReplace"
	ToolNameRenameMemory  = "MemoryRename"
	ToolNameDeleteMemory  = "MemoryDelete"

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
	ToolNameUpdateScheduledTask = "UpdateScheduledTask"
	ToolNameStopScheduledTask   = "StopThisScheduledTask"
	ToolNameRescheduleTask      = "RescheduleThisScheduledTask"

	ToolNameListPlaybooks = "ListPlaybooks"
	ToolNameWritePlaybook = "WritePlaybook"

	// The subagent tools are registered by core/subagent (they need the
	// agent itself, which this package must not import) and excluded from
	// the SUBAGENT scenario by name, which is the depth cap: a subagent
	// cannot start one of its own.
	ToolNameStartSubagent  = "StartSubagent"
	ToolNameWaitSubagent   = "WaitForSubagent"
	ToolNameCancelSubagent = "CancelSubagent"
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

// Builtin is a family of built-in tools exposed as one list. Every family —
// single-tool ones included — implements it, so registration and Compose
// treat them all the same way.
type Builtin interface {
	List() []tool.InvokableTool
}

// Builtins names the process-wide built-in families to register. A nil
// field means the family is not registered. The per-user families (file,
// memory, skill, todo, ask, publish, clock) are not here: they are built
// per run by Provider.Compose, which is their registration.
type Builtins struct {
	// Sandbox enables the shell and credential tools for one sandbox
	// backend. SandboxConfig.Credentials is filled from the provider's
	// store when nil.
	Sandbox       Sandbox
	SandboxConfig SandboxToolsConfig
}

// RegisterBuiltins constructs and registers every built-in family whose
// dependencies are provided. Call it during application setup, before the
// first Fire.
func RegisterBuiltins(p *Provider, b Builtins) error {
	if b.Sandbox == nil {
		return nil
	}
	return p.RegisterSandbox(b.Sandbox, b.SandboxConfig)
}

// CurrentDateTimeTools is the clock tool. The default system prompt sends
// the model here whenever an answer depends on "today" or "in two hours": a
// model's sense of now is its training cutoff, which is never now.
type CurrentDateTimeTools struct{}

// NewCurrentDateTimeTools returns the clock tools.
func NewCurrentDateTimeTools() *CurrentDateTimeTools {
	return &CurrentDateTimeTools{}
}

// List lists the clock tools, satisfying Builtin.
func (*CurrentDateTimeTools) List() []tool.InvokableTool {
	type output struct {
		DateTime string `json:"dateTime"`
	}
	return []tool.InvokableTool{MustTool(utils.InferTool(ToolNameCurrentDateTime,
		"Get the current date and time, with timezone. Call this whenever the answer depends on the current date or time, including relative expressions like today, this week or in two hours.",
		func(ctx context.Context, _ struct{}) (output, error) {
			return output{DateTime: time.Now().Format(time.RFC3339)}, nil
		}))}
}
