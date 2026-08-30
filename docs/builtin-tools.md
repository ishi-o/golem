# Built-in tools

Every built-in family exposes itself through one method:

```go
// core/tools
type Builtin interface {
    List() []tool.InvokableTool
}
```

One type per family — single-tool families included — so registration and
composition treat them all the same way.

## Families

| Family | Type | Tools |
| --- | --- | --- |
| Clock | `CurrentDateTimeTools` | `CurrentDateTime` |
| Files | `FileSystemTools` | `ReadFile`, `WriteFile`, `ListFiles`, `GrepFiles` |
| Memory | `MemoryTools` | `MemoryView`, `MemoryWrite` |
| Skills | `SkillTools` | `ListSkills`, `ReadSkillFile`, `WriteSkillFile`, `DeleteSkill` |
| Planning | `TodoWriteTools` | `TodoWrite` |
| Ask | `AskUserQuestionTools` | `AskUserQuestionTool` |
| Publish | `PublishFileTools` | `PublishFile`, `UpdatePublishedFile`, `UnpublishFile`, `RenewPublishedFile` |
| Shell | `SandboxTools` | `Bash`, `BashOutput`, `KillShell`, `RestartShellContainer`/`RestartShellPod` |
| Credentials | `CredentialTools` | `SetCredential`, `ListCredentials`, `DeleteCredential` |
| Schedule | `schedule.Tools` | `CreateScheduledTask`, `ListScheduledTasks`, `CancelScheduledTask` |
| Subagents | `subagent.Tools` | `StartSubagent`, `WaitForSubagent`, `CancelSubagent` |

External MCP tools can be registered alongside the built-ins — see
[Extending](extending.md#mcp-servers).

## Two kinds of family

**Per-user families** — clock, files, memory, skills, planning, ask,
publish — are built per run by the provider's `Compose`, rooted at the
asking user's workspace and wired to that run's handlers. They are not
registered and cannot be: their inputs only exist per run. You get them
automatically from `tools.NewProvider`.

**Process-wide families** — shell, credentials, schedule, subagents — are
registered once during application setup, in one explicit call:

```go
err := tools.RegisterBuiltins(provider, tools.Builtins{
    Sandbox:       dockerSandbox,               // nil field = family not registered
    SandboxConfig: golemtools.SandboxToolsConfig{}, // or docker.DefaultToolsConfig()
})
```

The schedule family has its own registration in `core/schedule` (it needs
the runner) — see [Scheduled tasks](scheduled-tasks.md) — and the subagent
family in `core/subagent` (it needs the agent) — see
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

`agent.ScheduledTaskScenario` excludes the three schedule tools by name so
a firing cannot schedule more work; `agent.ChatScenario` offers everything.

## Adding your own

See [Extending](extending.md#custom-tools) — a downstream tool is one
function plus `provider.Register`.
