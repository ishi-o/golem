# HTTP service (out of the box)

The `app` module serves the runtime over HTTP with [chi](https://github.com/go-chi/chi):
a streaming chat endpoint, cancellation, and health checks — built from the
same environment-driven bootstrap as the [CLI](cli.md).

```sh
export OPENAI_API_KEY=your-api-key
export OPENAI_MODEL=your-model

(cd app && go run ./cmd/golem)   # listens on :8080
```

`GOLEM_HTTP_ADDR` changes the address.

## Endpoints

```sh
# Streaming chat (server-sent events)
curl -N -X POST http://localhost:8080/api/agent/chat \
  -H 'Content-Type: application/json' \
  -d '{"message":"hello"}'

# Cancel a run
curl -X POST http://localhost:8080/api/agent/cancel \
  -H 'Content-Type: application/json' \
  -d '{"request_id":"your-request-id"}'

# Liveness and readiness
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
```

## What is wired

- The per-user families (file, memory, skill, todo, ask, publish, clock)
  are composed per run. Note the ask tool needs a question handler on the
  run; the stock service registers none, so it is not offered — a surface
  that can host questions adds one via the listener (see
  [Library guide](library.md#user-questions)).
- SQLite persistence, as the CLI.
- The shell sandbox when `GOLEM_SANDBOX` is set — docker or kubernetes
  ([reference](configuration.md#sandbox-golem_sandbox)).
- Scheduled tasks are not offered (injection-only; see
  [Scheduled tasks](scheduled-tasks.md)).
- The router accepts two optional handlers an embedding application can
  mount: `ShareHandler` (serving published files under `/share/*`) and
  `FeishuHandler` (the Feishu/Lark webhook under `/webhooks/feishu`).

Deployment note: for published-file links to resolve, run behind or beside
a `ShareHandler`-mounting surface and set `GOLEM_PUBLISH_BASE_URL`.

See the [configuration reference](configuration.md) for every variable.
