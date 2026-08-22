# golem

golem is a Go runtime for building tool-using AI assistants. It runs model
turns through [eino](https://github.com/cloudwego/eino), keeps conversation
state, composes the tool set per run, streams responses to surfaces, and
supports cancellation, graceful shutdown, and run-lifecycle listeners.

## Quick start

**As a library.** Import `github.com/ishi-o/golem/core` (plus one store
implementation), populate the config structs, build the tool provider and
the agent, and fire runs from your own application. Everything the runtime
does — built-in tools, scheduled tasks, user questions, file publishing —
is assembled and injected by you. → [Library guide](docs/library.md)

**As an out-of-the-box CLI.** The `cmd` module builds a chat client on an
OpenAI-compatible model and a SQLite store, with terminal streaming and
inline user questions. Set two environment variables and talk to it. →
[CLI](docs/cli.md)

**As an out-of-the-box service.** The `app` module serves the same runtime
over HTTP: a streaming chat endpoint, cancellation, and health checks, with
an env-selected shell sandbox (docker or kubernetes). →
[HTTP service](docs/http-service.md)

## Documentation

| Page                                       | What it covers                                                                       |
| ------------------------------------------ | ------------------------------------------------------------------------------------ |
| [Library guide](docs/library.md)           | Embedding golem: config, stores, provider, built-in tools, agent, runs and listeners |
| [Built-in tools](docs/builtin-tools.md)    | Every built-in family, the `Builtin` interface, registration and scenario filtering  |
| [Scheduled tasks](docs/scheduled-tasks.md) | The `schedule.Scheduler` injection seam, task lifecycle and statuses                 |
| [Sandbox](docs/sandbox.md)                 | The shell tools, the docker and kubernetes backends, env wiring                      |
| [Stores](docs/stores.md)                   | The persistence contract and the sqlx / mongodb / redis adapters                     |
| [Configuration](docs/configuration.md)     | Every environment variable the shipped binaries read                                 |
| [CLI](docs/cli.md)                         | The out-of-the-box command-line client                                               |
| [HTTP service](docs/http-service.md)       | The out-of-the-box HTTP server and its endpoints                                     |
| [Extending](docs/extending.md)             | Custom tools, MCP servers, todo and question handlers, connectors                    |
