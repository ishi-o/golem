# golem

[golem](https://github.com/ishi-o/golem) is a Go runtime for building
tool-using AI assistants. It runs model turns through [eino](https://github.com/cloudwego/eino),
keeps conversation state, exposes tools, streams responses to different
surfaces, and supports cancellation, graceful shutdown, and listeners for
run lifecycle, streamed content, usage, errors, and outcomes.

It provides an embeddable agent core with conversation memory, streaming
callbacks, cancellation, scheduled tasks, user questions, file publishing,
[mcp](https://github.com/modelcontextprotocol/modelcontextprotocol) servers,
and tool search. Persistence adapters cover SQL databases through
[sqlx](https://github.com/jmoiron/sqlx), [mongodb](https://github.com/mongodb/mongo-go-driver),
and [redis](https://github.com/redis/go-redis). Applications can expose the
runtime through an HTTP surface built with [chi](https://github.com/go-chi/chi)
or a CLI built with [cobra](https://github.com/spf13/cobra), with structured
logs from the standard library's [log/slog](https://github.com/golang/go/tree/master/src/log/slog).

The [feishu](https://open.feishu.cn/) / lark connector is optional. When it is
wired into an application, it handles message events, interactive cards,
replies, and duplicate-message protection.

## Built-in tools

The runtime supports these built-in tools; applications can enable or register
them according to their wiring and configuration:

- Time: `CurrentDateTime`
- Files: `ReadFile`, `WriteFile`, `ListFiles`, `GrepFiles`
- Memory: `MemoryView`, `MemoryWrite`
- Skills: `ListSkills`, `ReadSkillFile`, `WriteSkillFile`, `DeleteSkill`
- Planning: `TodoWrite`
- Published files: `PublishFile`, `UpdatePublishedFile`, `UnpublishFile`,
  `RenewPublishedFile`
- User interaction: `AskUserQuestionTool`
- Scheduled tasks: `CreateScheduledTask`, `ListScheduledTasks`,
  `CancelScheduledTask`
- Tool discovery: `tool_search`
- External MCP tools can be registered alongside the built-ins.

## Use as an upstream library

golem can be used as an upstream library. Import `core` and at least one store
implementation; `core` exposes the agent and persistence interfaces, while the
selected store supplies persistence and conversation memory.

```sh
go get github.com/ishi-o/golem/core
go get github.com/ishi-o/golem/store/sqlx
```

## Adding a listener

`Fire` returns after the run has been accepted. A listener receives the
accumulated content and the final outcome:

```go
listener := agent.ListenerFuncs{
	OnContentF: func(content string) {
		fmt.Print("\r", content)
	},
	OnErrorF: func(err error) {
		log.Printf("agent failed: %v", err)
	},
	OnFinishedF: func(outcome agent.Outcome) {
		log.Printf("agent finished: %s", outcome)
	},
}

request := agent.NewRequest(
	agent.ChatScenario,
	text,
	agent.WithRequestID(runID),
	agent.WithIdentity(userID, chatID, "p2p"),
	agent.WithConversation(chatID, rootMessageID, replyMessageID),
	agent.WithListener(listener),
)
if err := runtime.Fire(request); err != nil {
	log.Fatal(err)
}
```

Set the same listener in `runtime.DeclaredListeners` when it should observe
every run, including scheduled runs.

## Adding a tool

Register a downstream tool during wiring. The request identity is available
through the typed tool context:

```go
type profileInput struct{}

func profileTool() tool.InvokableTool {
	return golemtools.MustTool(utils.InferTool(
		"MyProfile",
		"Return the profile of the person in this conversation.",
		func(ctx context.Context, _ profileInput) (string, error) {
			userID, err := golemtools.UserID.Require(ctx)
			if err != nil {
				return "", err
			}
			return "profile for " + userID, nil
		},
	))
}

provider.Register(profileTool(), nil)
```

## Adding an mcp server

The provider accepts an `MCPBuilder`, so a downstream integration can choose
its own mcp client and return the tools it connected for the current user:

```go
type mcpBuilder struct {
	connect func(context.Context, string, string) ([]tool.InvokableTool, io.Closer, error)
}

func (b mcpBuilder) Build(ctx context.Context, userID, chatID string) (golemtools.MCPTools, error) {
	serverTools, closer, err := b.connect(ctx, userID, chatID)
	if err != nil {
		return golemtools.MCPTools{}, err
	}
	return golemtools.MCPTools{Tools: serverTools, Closer: closer}, nil
}

provider := golemtools.NewProvider(cfg, workspaces, backend, mcpBuilder{
	connect: connectMCP,
})
```

## Quick start

Requirements: Go 1.26 or newer.

```sh
make build test lint
(cd app && go run ./cmd/golem)
(cd cmd && go run ./golem version)
```

The HTTP application listens on `:8080` by default; set `GOLEM_HTTP_ADDR` to
change it. Applications construct a model and one of the store implementations,
then inject the resulting agent and handlers into the app or CLI surface.

## Configuration

`core/config` reads these environment variables and applies sensible defaults:

| Variable                      | Purpose                                                |
| ----------------------------- | ------------------------------------------------------ |
| `GOLEM_LOCALE`                | Language used by agent-generated runtime messages      |
| `GOLEM_STORAGE_LOCATION`      | Root directory for user workspaces; defaults to `data` |
| `GOLEM_STORAGE_BASE_URL`      | Base URL for published files                           |
| `GOLEM_STORAGE_CDN_URL`       | Optional CDN base URL for published files              |
| `GOLEM_ADMINS`                | Comma-separated administrator IDs                      |
| `GOLEM_ASK_USER_ENABLED`      | Enable interactive questions                           |
| `GOLEM_ASK_USER_TTL`          | Question lifetime as a Go duration                     |
| `GOLEM_PUBLISH_BASE_URL`      | Base URL emitted by the publish-file tool              |
| `GOLEM_GUIDE_THRESHOLD`       | Tool-result size threshold for file-backed responses   |
| `GOLEM_TOOL_SEARCH_RESULTS`   | Maximum results returned by tool search                |
| `GOLEM_TOOL_SEARCH_THRESHOLD` | Tool count at which tool search is enabled             |
| `GOLEM_MCP_TRUSTED_HOSTS`     | Comma-separated MCP host allowlist                     |
