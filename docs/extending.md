# Extending

## Custom tools

Register a downstream tool during application setup. The request identity
is available through the typed tool context:

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

`utils.InferTool` (from eino) derives the schema from the input struct;
`MustTool` panics on an uninferable schema — a programming error, caught at
first use. Identity keys: `UserID`, `ChatID`, `ChatType`, `RootMessageID`,
`ReplyMessageID`. The second `Register` argument optionally gates the tool
by scenario (see [Built-in tools](builtin-tools.md#scenario-filtering)).

A custom tool can also intercept results — `tools.Interceptor` wraps every
tool the run offers; the large-response interceptor core installs is one.

## MCP servers

The provider accepts an `MCPBuilder`, so a downstream connector chooses its
own MCP client and returns the tools it connected for the current user:

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

Connections are built per run (MCP handshakes are blocking network work) and
closed when the run ends; a failing server costs its tools, never the run.
`core/toolsearch` (an index over tool descriptions for large tool sets) is
not yet wired to any shipped surface.

## Todo handlers

The model records plans through `TodoWrite`; every event is delivered to
the run's todo handlers. Add one when the run starts:

```go
agent.WithListener(agent.ListenerFuncs{
    OnStartFunc: func(run *agent.RunContext) {
        run.AddTodoHandler(func(ctx context.Context, event tools.TodoEvent) {
            renderTodoCard(event.Todos) // your surface
        })
    },
})
```

`tools.TodoFanOut` combines handlers; a panicking handler costs only its
own view.

## Question handlers

The ask tool needs a `tools.QuestionHandler` on the run — see
[User questions](library.md#user-questions). Handlers that return answers
inline implement `tools.InlineAnswers`; others end the turn (`tools.ErrNotAnswered`
marks "no answer inside this run"), and the answers arrive in a later run.

## Connectors

A connector translates between a chat surface and the agent API: receive a
message → `agent.Fire` with identity and conversation ids → stream
`OnContent` to the surface → deliver answers to outstanding asks. The
repository's `connector/feishu` (Feishu/Lark: message events, cards,
replies, duplicate-message protection) is the reference implementation.

## Sandboxes and stores

New persistence adapters implement `store.Backend` +
`chatmemory.Repository`; new sandbox backends implement `tools.Sandbox`
(`Ensure`/`Remove`/`Close`) and register through
`tools.RegisterBuiltins`. Both are plain interfaces — no registration
machinery beyond the provider.
