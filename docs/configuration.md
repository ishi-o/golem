# Configuration

Two configuration surfaces exist, one per shape of using golem:

- **As a library**, you populate the `core/config` structs from any source
  and call `Normalize` — no environment variable is read.
- **The shipped binaries** (the [CLI](cli.md) and the
  [HTTP service](http-service.md)) read the variables below through
  `internal/bootstrap` and apply the same defaults.

The `GOLEM_TOOL_SEARCH_*` and `GOLEM_MCP_TRUSTED_HOSTS` variables are read
but consumed by nothing yet — the tool-search and MCP integrations are
unwired in the shipped binaries.

## Model and store (both binaries)

| Variable | Purpose |
| --- | --- |
| `OPENAI_API_KEY` | API key for the app and CLI model |
| `OPENAI_MODEL` | Model name used by the app and CLI |
| `OPENAI_BASE_URL` | Optional OpenAI-compatible API base URL |
| `GOLEM_SQLITE_PATH` | Optional SQLite database path (default `data/golem.db`) |

## Runtime (`GOLEM_*`)

| Variable | Purpose |
| --- | --- |
| `GOLEM_HTTP_ADDR` | HTTP listen address (service only; default `:8080`) |
| `GOLEM_LOCALE` | Language used by agent-generated runtime messages |
| `GOLEM_STORAGE_LOCATION` | Root directory for user workspaces; defaults to `data` |
| `GOLEM_STORAGE_BASE_URL` | Base URL for published files |
| `GOLEM_STORAGE_CDN_URL` | Optional CDN base URL for published files |
| `GOLEM_ADMINS` | Comma-separated administrator IDs |
| `GOLEM_ASK_USER_ENABLED` | Offer the ask tool (default true) |
| `GOLEM_ASK_USER_TTL` | Question lifetime as a Go duration |
| `GOLEM_PUBLISH_BASE_URL` | Base URL emitted by the publish-file tool |
| `GOLEM_GUIDE_THRESHOLD` | Tool-result size threshold for file-backed responses |
| `GOLEM_TOOL_SEARCH_RESULTS` | Maximum results returned by tool search (unwired) |
| `GOLEM_TOOL_SEARCH_THRESHOLD` | Tool count at which tool search is enabled (unwired) |
| `GOLEM_MCP_TRUSTED_HOSTS` | Comma-separated MCP host allowlist (unwired) |

## Sandbox (`GOLEM_SANDBOX*`)

`GOLEM_SANDBOX` selects the shell sandbox backend: `docker`,
`kubernetes`, or unset — no shell tools. Misconfiguration (a backend
selected without its required variables) fails startup rather than silently
offering nothing.

| Variable | Purpose |
| --- | --- |
| `GOLEM_SANDBOX` | Backend: `docker`, `kubernetes`, or unset (off) |
| `GOLEM_SANDBOX_IMAGE` | Sandbox container image (required when a backend is set) |
| `GOLEM_SANDBOX_NETWORK` | Docker network for sandbox containers (docker only) |
| `GOLEM_SANDBOX_NAMESPACE` | Kubernetes namespace for sandbox Jobs (default `default`) |
| `GOLEM_SANDBOX_WORKING_DIR` | Kubernetes working directory, the PVC mount path (kubernetes only) |
| `GOLEM_SANDBOX_PVC` | Kubernetes PVC holding user workspaces (kubernetes only) |
| `GOLEM_SANDBOX_PVC_SUBPATH` | Optional per-user subpath prefix in the PVC (kubernetes only) |

Docker sandboxes additionally honour the standard Docker environment
(`DOCKER_HOST`, TLS variables); kubernetes sandboxes use in-cluster config,
then `KUBECONFIG`/`~/.kube/config`.

Scheduled tasks are injection-only in every shape: the binaries offer the
schedule tools only when a deployment extends the bootstrap with a
`schedule.Scheduler` — see [Scheduled tasks](scheduled-tasks.md).
