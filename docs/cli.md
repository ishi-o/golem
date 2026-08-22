# CLI (out of the box)

The `cmd` module builds a terminal chat client on an OpenAI-compatible
model and a SQLite store — an out-of-the-box agent with streaming output,
inline user questions, and cancellation. No code required.

```sh
export OPENAI_API_KEY=your-api-key
export OPENAI_MODEL=your-model
# export OPENAI_BASE_URL=https://your-compatible-endpoint/v1

(cd cmd && go run ./golem chat "hello")
```

## Commands

```sh
golem chat "plan a trip to Kyoto"   # one run, streamed to the terminal
golem chat --request-id my-run ...  # a run you can cancel by name
golem cancel my-run                 # abort a run in flight
golem version
```

The `chat` command fires a `ChatScenario` run as user `local`; the ask tool
is offered with a terminal handler, so the model can put questions to you
and continue from your answers in the same run.

## What is wired

- The per-user families (file, memory, skill, todo, ask, publish, clock)
  are composed per run automatically.
- SQLite persistence under `GOLEM_STORAGE_LOCATION` (default `data`), or
  `GOLEM_SQLITE_PATH` for the database file itself.
- The shell sandbox when `GOLEM_SANDBOX` is set — the same env contract as
  the [HTTP service](configuration.md#sandbox-golem_sandbox).
- Scheduled tasks are not offered: no scheduler is injected. Use golem
  [as a library](library.md) (or extend the bootstrap) to add one.

See the [configuration reference](configuration.md) for every variable.
