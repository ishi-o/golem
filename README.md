# golem

[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8)](#)

A tool-using agent runtime on [eino](https://github.com/cloudwego/eino): a Go
port of [spring-agent](https://github.com/kezhenxu94/spring-agent)'s
architecture — one entry point for running the agent, tool composition,
system-prompt rendering, MCP servers, scheduled tasks, chat memory, file
publishing and a searchable tool index — behind one entry point, with
pluggable persistence (sqlx/SQLite, MongoDB, Redis) and a Feishu surface.

Run the server or the command line as they are, or take the libraries and give
the agent a surface of your own.

## Modules

```
core                    the agent runtime; backend-agnostic, holds no database driver
persistence/sqlx        one backend: SQLite through sqlx (no server needed)
persistence/mongodb     one backend: MongoDB
persistence/redis       one backend: Redis (noeviction + AOF required)
integration/feishu      the Feishu/Lark surface (websocket events, reply cards, tools)
app                     deployable server; depends on every backend module
cli                     laptop command line; sqlx + inline questions only
```

## As an SDK

Take `golem/core` plus exactly one persistence module — core is
backend-agnostic and holds no database driver, and the module is what supplies
both the repositories and the chat memory store:

```sh
go get github.com/ishi-o/golem/core
go get github.com/ishi-o/golem/persistence/sqlx
```

Go has no `compileOnly`; the module boundary is what keeps a consumer of core
from inheriting the MongoDB driver, or Redis, or SQLite. That is why this
repository is one Go module per library rather than one module with packages.

## Running

The server and the CLI read the same environment variables spring-agent does
— `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `OPENAI_MODEL`, plus
`EMBEDDING_BASE_URL`, `EMBEDDING_API_KEY`, `EMBEDDING_MODEL` — from `.env` or
the environment. `PERSISTENCE_TYPE` (`sqlx` | `mongodb` | `redis`) selects the
backend the app wires; the CLI always uses sqlx under `~/.golem/`.

```sh
docker compose --profile mongodb up   # or: redis, or neither
(cd app && go run .)
(cd cli && go run .)
```

## Status

A port in progress; see the module godoc for each subsystem. Doc/sheet/wiki
Feishu tools, sandboxed shell backends (docker/kubernetes) and Milvus are not
ported.
