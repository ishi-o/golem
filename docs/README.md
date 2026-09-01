# golem

golem is a Go runtime for building tool-using AI assistants. It runs model
turns through [eino](https://github.com/cloudwego/eino), keeps conversation
state, composes the tool set per run, streams responses to surfaces, and
supports cancellation, graceful shutdown, and run-lifecycle listeners.

## Quick start

**As a library.** Import `github.com/ishi-o/golem/core` (plus one store
implementation), populate the config structs, build the tool provider and
the agent, and fire runs from your own application. Everything the runtime
does — built-in tools, scoped knowledge, external events, scheduled tasks,
user questions, file publishing —
is assembled and injected by you. → [Library guide](library.md)

**As an out-of-the-box CLI.** The companion
[golem-cli](https://github.com/ishi-o/golem-cli) repository builds a chat
client on an OpenAI-compatible model and a SQLite store, with terminal
streaming and inline user questions.

## Documentation

| Page                                  | What it covers                                                                       |
| ------------------------------------- | ------------------------------------------------------------------------------------ |
| [Library guide](library.md)           | Embedding golem: config, stores, provider, built-in tools, agent, runs and listeners |
| [Built-in tools](builtin-tools.md)    | Every built-in family, the `Builtin` interface, registration and scenario filtering  |
| [Scheduled tasks](scheduled-tasks.md) | The `schedule.Scheduler` injection seam, task lifecycle and statuses                 |
| [Knowledge and events](library.md#knowledge-and-external-events) | Scoped Eino knowledge, fixed event playbooks, intake, triage, and event tools |
| [Sandbox](sandbox.md)                 | The shell tools, the docker and kubernetes backends, env wiring                      |
| [Stores](stores.md)                   | The persistence contract and the sqlx / mongodb / redis adapters                     |
| [Extending](extending.md)             | Custom tools, MCP servers, todo and question handlers, connectors                    |
| [Feishu connector](feishu.md)         | The optional Feishu/Lark chat surface                                                |
