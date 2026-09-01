# Built-in tools

Every built-in family exposes itself through one method:

```go
// core/tools
type Builtin interface {
    List() []tool.InvokableTool
}
```

`tools.Provider.Compose` gives these Eino tools to `compose.ToolsNode` for
dispatch and standard `schema.ToolMessage` construction. The node is
configured for sequential execution; provider middleware supplies large-result
handling and model-visible tool errors, while application-specific behavior can
use native Eino tool middleware.

One type per family — single-tool families included — so registration and
composition treat them all the same way.

## Families

| Family | Type | Tools |
| --- | --- | --- |
| Clock | `CurrentDateTimeTools` | `CurrentDateTime` |
| Files | `FileSystemTools` | `ReadFile`, `WriteFile`, `ListFiles`, `GrepFiles` |
| Memory | `MemoryTools` | `MemoryView`, `MemoryWrite`, `MemoryCreate`, `MemoryInsert`, `MemoryStrReplace`, `MemoryRename`, `MemoryDelete` |
| Skills | `SkillTools` | `ListSkills`, `ReadSkillFile`, `WriteSkillFile`, `DeleteSkill` |
| Planning | `TodoWriteTools` | `TodoWrite` |
| Ask | `AskUserQuestionTools` | `AskUserQuestionTool` |
| Publish | `PublishFileTools` | `PublishFile`, `UpdatePublishedFile`, `UnpublishFile`, `RenewPublishedFile` |
| Shell | `SandboxTools` | `Bash`, `BashOutput`, `KillShell`, `RestartShellContainer`/`RestartShellPod` |
| Credentials | `CredentialTools` | `SetCredential`, `ListCredentials`, `DeleteCredential` |
| Knowledge | `knowledge.Tools` | `ListKnowledgeBase`, `IndexKnowledge`, `SearchKnowledge`, `UpdateKnowledgeScope`, `DeleteKnowledge` |
| Knowledge administration | `knowledge.AdminTools` | `ListOwnerKnowledgeBase`, `SearchOwnerKnowledge` |
| Schedule | `schedule.Tools` | `CreateScheduledTask`, `ListScheduledTasks`, `CancelScheduledTask`, `UpdateScheduledTask`, `StopThisScheduledTask`, `RescheduleThisScheduledTask` |
| Subagents | `subagent.Tools` | `StartSubagent`, `WaitForSubagent`, `CancelSubagent` |
| External events | `events.Tools` | `ListOpenSituations`, `GetSituationEvents`, `RecordSituationAssessment`, `ResolveSituation` |
| Event playbooks | `events.PlaybookTools` | `ListPlaybooks`, `WritePlaybook` |

External MCP tools can be registered alongside the built-ins — see
[Extending](extending.md#mcp-servers).

## Two kinds of family

**Per-user families** — clock, files, memory, skills, planning, ask,
publish — are built per run by the provider's `Compose`, rooted at the
asking user's workspace and wired to that run's handlers. They are not
registered and cannot be: their inputs only exist per run. You get them
automatically from `tools.NewProvider`.

**Explicit families** — knowledge, external events, event playbooks, shell,
credentials, schedule, and subagents — are registered once during application
setup because they carry process-wide dependencies:

```go
err := tools.RegisterBuiltins(provider, tools.Builtins{
    Sandbox:       dockerSandbox,               // nil field = family not registered
    SandboxConfig: golemtools.SandboxToolsConfig{}, // or docker.DefaultToolsConfig()
})
```

The knowledge and event families are registered from `core/knowledge` and
`core/events`; playbook tools require an application-supplied administrator
check. The schedule family has its own registration in `core/schedule` (it
needs the runner) — see [Scheduled tasks](scheduled-tasks.md) — and the
subagent family in `core/subagent` (it needs the agent) — see
[Subagents](subagents.md). Credential tools
join the shell family automatically when a credential store is configured:
`RegisterBuiltins` fills `SandboxConfig.Credentials` from the provider's
store when nil.

## Scenario filtering

A registered tool can carry an `offers func(name string) bool` gate, and
every run filters the whole set through its scenario's `Offers` — the two
compose as an AND:

```go
provider.Register(myTool, nil) // nil offers: every scenario decides alone
```

`agent.ScheduledTaskScenario` excludes the tools that create or manage other
tasks, while allowing the two self-control tools for its current firing.
`agent.SubagentScenario` excludes subagent and all schedule tools. Event
triage additionally excludes administrator knowledge and playbook mutation
tools. `agent.ChatScenario` omits self-control tools because it is not a task
firing.

## Adding your own

See [Extending](extending.md#custom-tools) — a downstream tool is one
function plus `provider.Register`.
