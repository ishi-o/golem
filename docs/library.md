# Library guide

golem's core is a library: `github.com/ishi-o/golem/core` depends on nothing
but [eino](https://github.com/cloudwego/eino), and every heavyweight driver
(model SDKs, databases, container clients) lives in its own module outside
core. You assemble the runtime yourself — this page shows the whole path
from an empty `main` to a firing agent.

1. [Install](#install)
2. [Configuration](#configuration)
3. [Store](#store)
4. [Model](#model)
5. [Tool provider and built-in tools](#tool-provider-and-built-in-tools)
6. [The agent](#the-agent)
7. [Knowledge and external events](#knowledge-and-external-events)
8. [Runs, listeners, cancellation](#runs-listeners-cancellation)
9. [User questions](#user-questions)
10. [A complete minimal program](#a-complete-minimal-program)

## Install

```sh
go get github.com/ishi-o/golem/core
go get github.com/ishi-o/golem/store/sqlx   # or mongodb / redis
```

## Configuration

`core/config` is plain structs — populate them from a file, flags, or code,
then call `Normalize` to fill defaults and validate:

```go
cfg := config.Config{
    Locale: "en",
    Storage: config.Storage{Location: "data"},
}
if err := cfg.Normalize(); err != nil {
    log.Fatal(err)
}
```

`SystemPrompt` and `ScheduledTaskPrompt` have defaults you can override;
`cfg.AI.GuideThreshold` sets the tool-result size above which oversized
results are diverted to a file in the user's workspace. (The `GOLEM_*`
environment variables belong to [golem-cli](https://github.com/ishi-o/golem-cli),
not to the library.)

## Store

Pick one persistence adapter; each implements both `store.Backend` (the
record stores) and `chatmemory.Repository` (conversation memory):

```go
db, _ := sqlx.Open("sqlite3", "golem.db")
backend, err := sqlxstore.New(db, sqlxstore.WithDialect(sqlxstore.DialectSQLite))
if err != nil { log.Fatal(err) }
if err := backend.Migrate(ctx); err != nil { log.Fatal(err) }
```

The mongodb and redis adapters have the same shape — see
[Stores](stores.md).

## Model

The agent takes any Eino `model.ToolCallingChatModel`. Core deliberately
ships no provider-specific model. Use Eino-ext directly for OpenAI-compatible
models:

```go
import einoopenai "github.com/cloudwego/eino-ext/components/model/openai"

chatModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
    APIKey: apiKey,
    Model:  modelName,
    BaseURL: baseURL, // any OpenAI-compatible endpoint
})
```

## Tool provider and built-in tools

`tools.NewProvider` assembles each run's tool set: the per-user families
(file, memory, skill, todo, ask, publish, clock — built per run against the
user's workspace), any registered tools, and per-user MCP servers.

```go
workspaces := storage.NewWorkspaceFactory(cfg.Storage.Location)
provider := tools.NewProvider(cfg, workspaces, backend, nil, // nil MCPBuilder
    tools.WithLogger(logger))
```

Each run is backed by Eino's `compose.ToolsNode`. The provider passes the
original Eino tools to that node and installs golem's cross-cutting policies
as Eino tool middleware. Calls stay sequential because built-in tools can
write shared state; the agent's queueing, cancellation, and turn-ending
rules remain outside the node.

The process-wide families are registered in one explicit call. Sandbox
tools need a backend from [sandbox/](../sandbox); scheduled tasks need an
injected [Scheduler](scheduled-tasks.md):

```go
// Shell tools (optional): one docker or kubernetes sandbox.
sandbox, err := docker.New(docker.Config{
    Image:      "your-shell-runner-image",
    Workspaces: workspaces,
    Credentials: tools.CredentialsFromRepository(backend.ShellCredentials()),
})
if err != nil { log.Fatal(err) }
if err := tools.RegisterBuiltins(provider, tools.Builtins{
    Sandbox:       sandbox,
    SandboxConfig: docker.DefaultToolsConfig(),
}); err != nil { log.Fatal(err) }

// Scheduled tasks (optional): inject a scheduler you implement.
runner, err := schedule.New(backend.ScheduledTasks(), agent, schedule.Config{
    Prompt:    cfg.AI.ScheduledTaskPrompt,
    Scheduler: yourScheduler,
}, logger)
if err != nil { log.Fatal(err) }
if err := runner.Start(ctx); err != nil { log.Fatal(err) }
schedule.RegisterBuiltins(provider, schedule.NewTools(runner, backend.ScheduledTasks()))

// Subagents (optional): the agent as a tool of its own. See Subagents.
subagent.Register(provider, agent, cfg, nil, logger)
```

## Knowledge and external events

Knowledge is an optional facade backed by an Eino-native implementation. The
reference in-memory implementation is useful for tests and small processes;
production applications can implement `knowledge.KnowledgeBase` in another
module without changing the agent:

```go
base := knowledge.NewInMemory(knowledge.InMemoryOptions{})
a := agent.New(chatModel, backend, provider, cfg,
    agent.WithBackend(backend),
    agent.WithKnowledgeBase(base, knowledge.RetrievalConfig{TopK: 4, MaxChars: 12000}),
)
for _, t := range knowledge.NewTools(base, workspaces.ForOwner("admin")).List() {
    provider.Register(t, nil)
}
```

The agent retrieves scoped knowledge before ordinary model turns and frames
the passages as untrusted reference data. Explicit `KnowledgeRetrieval`
requests can pin a different scope and filter, which is how unattended event
triage uses a source owner's fixed playbook rather than event text as a query.

External events are an optional intake/sweeper pair. Intake only normalizes,
deduplicates, correlates, and stores bounded evidence; the sweeper performs
the model call later:

```go
eventCfg := events.Config{Enabled: true, Sources: map[string]events.SourceConfig{
    "monitor": {
        Owner: events.Owner{UserID: "triage-owner"},
        Playbook: events.Playbook{Query: "database incident", DocIDs: []string{"runbook"}},
    },
}}
intake, _ := events.NewIntake(eventCfg, backend.Situations(), backend.ObservedEvents(), backend.ProcessedMessages())
sweeper, _ := events.NewSweeper(eventCfg, a, backend.Situations(), backend.ObservedEvents(), backend.ProcessedMessages())
sweeper.Start(ctx)
for _, t := range events.NewTools(backend.Situations(), backend.ObservedEvents()).List() {
    provider.Register(t, nil)
}
for _, t := range events.NewPlaybookTools(base, eventCfg, isAdmin).List() {
    provider.Register(t, nil)
}
```

Connectors call `intake.Observe` after authenticating the source actor. The
playbook tools are administrator-only and write documents to the configured
owner's personal knowledge scope. `events.TriageScenario` is memoryless and
does not receive scheduling, subagent, administrator-knowledge, or playbook
mutation tools.

Your own tools register individually — see
[Extending](extending.md). The full family inventory is in
[Built-in tools](builtin-tools.md).

## The agent

```go
a := agent.New(chatModel, backend, provider, cfg,
    agent.WithBackend(backend),
    agent.WithLogger(logger),
    agent.WithModelName(modelName),
    agent.WithMemoryWindow(50),              // messages kept per conversation
    agent.WithDefaultListener(defaultListener), // observes every run
)
```

`Fire` is non-blocking (it returns once the run is accepted); `Cancel`
aborts a run by request id; `Shutdown` stops accepting and waits out
in-flight runs.

`FireOrQueue` offers a message to the run already in flight over the same
conversation by the same user: a correction, an addition, an answer to
"should I go on?" arriving while the agent works. True means the run took it
— it is read into the turn at the next tool boundary, framed so the model
knows it arrived mid-turn, and no run of its own is started. False means
nothing matching is live and the caller should `Fire` the request itself.
The run's listeners hear `OnMessageQueued` when a message joins and
`OnQueuedMessageRead` when it is read in; a message the run never got round
to reading is re-fired as its own run after the first one finishes.

## Runs, listeners, cancellation

```go
request := agent.NewRequest(
    agent.ChatScenario,
    text,
    agent.WithRequestID(runID),
    agent.WithIdentity(userID, chatID, "p2p"),
    agent.WithConversation(conversationID, rootMessageID, replyMessageID),
    agent.WithListener(agent.ListenerFuncs{
        OnContentFunc: func(soFar string) { fmt.Print(soFar) },
        OnErrorFunc:   func(err error) { log.Print(err) },
        OnFinishedFunc: func(outcome agent.Outcome) {
            // agent.OutcomeCompleted / OutcomeFailed / OutcomeCancelled
        },
    }),
)
if err := a.Fire(request); err != nil { log.Fatal(err) }
// later: a.Cancel(runID)
```

Scenarios shape the run: `ChatScenario` keeps conversation memory and offers
every tool; `ScheduledTaskScenario` (used by the schedule runner) resumes
the creating conversation but excludes the schedule tools so a firing
cannot schedule more work; `SubagentScenario` (used by the subagent tools)
joins no conversation and withholds the subagent and schedule tools, which
is the depth cap — see [Subagents](subagents.md). Event triage is a separate
memoryless scenario with the same unattended-work restrictions.

Listeners also carry `OnReasoning` (the model's thinking so far, one block
per model call — a reasoning-capable model's stream carries it) and
`OnSubagent` (what a run this one started is doing; only the parent's
listeners receive it).

## User questions

The ask tool is offered when the run carries a question handler and
`cfg.AI.Tools.AskUserQuestion.Enabled` is true (the default). Add the
handler when the run starts:

```go
agent.WithListener(agent.ListenerFuncs{
    OnStartFunc: func(run *agent.RunContext) {
        run.AddQuestionHandler(myHandler) // implements tools.QuestionHandler
    },
})
```

A handler that returns the answers inside the call (`tools.InlineAnswers`)
lets the model continue from them in the same run; any other handler ends
the turn, and the answers arrive in a later run. See the CLI's terminal
handler (`cmd/questions.go` in
[golem-cli](https://github.com/ishi-o/golem-cli)) for a working example.

## A complete minimal program

```go
package main

import (
    "context"
    "log"
    "log/slog"

    einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
    "github.com/ishi-o/golem/core/agent"
    "github.com/ishi-o/golem/core/config"
    "github.com/ishi-o/golem/core/storage"
    "github.com/ishi-o/golem/core/tools"
    sqlxstore "github.com/ishi-o/golem/store/sqlx"
    "github.com/jmoiron/sqlx"
    _ "github.com/mattn/go-sqlite3"
)

func main() {
    ctx := context.Background()
    logger := slog.Default()

    cfg := config.Config{}
    if err := cfg.Normalize(); err != nil { log.Fatal(err) }

    db, err := sqlx.Open("sqlite3", "golem.db")
    if err != nil { log.Fatal(err) }
    backend, err := sqlxstore.New(db, sqlxstore.WithDialect(sqlxstore.DialectSQLite))
    if err != nil { log.Fatal(err) }
    if err := backend.Migrate(ctx); err != nil { log.Fatal(err) }

    chatModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
        APIKey: "sk-...", Model: "gpt-4o",
    })
    if err != nil { log.Fatal(err) }

    workspaces := storage.NewWorkspaceFactory(cfg.Storage.Location)
    provider := tools.NewProvider(cfg, workspaces, backend, nil)
    a := agent.New(chatModel, backend, provider, cfg,
        agent.WithBackend(backend), agent.WithLogger(logger))

    err = a.Fire(agent.NewRequest(agent.ChatScenario, "hello!",
        agent.WithIdentity("user-1", "chat-1", "p2p"),
        agent.WithListener(agent.ListenerFuncs{
            OnContentFunc:  func(soFar string) { log.Print(soFar) },
            OnFinishedFunc: func(outcome agent.Outcome) { log.Print("done: ", outcome) },
        }),
    ))
    if err != nil { log.Fatal(err) }

    _ = a.Shutdown(ctx)
    _ = db.Close()
}
```

Next: [Built-in tools](builtin-tools.md) ·
[Scheduled tasks](scheduled-tasks.md) · [Extending](extending.md)
