# Stores

`core/store` defines the persistence contract — `Backend` — covering
conversation-reachable records: scheduled tasks, pending questions,
published resources, shell credentials, MCP server configs, processed
messages, observed event evidence, and correlated situations. Every adapter implements `Backend` **and**
`chatmemory.Repository` (conversation memory), so one constructor call
yields everything the runtime needs.

| Module | Requires | Constructor |
| --- | --- | --- |
| `github.com/ishi-o/golem/store/sqlx` | `github.com/jmoiron/sqlx` | `sqlxstore.New(db, WithDialect(...), WithTablePrefix(...))` |
| `github.com/ishi-o/golem/store/mongodb` | `go.mongodb.org/mongo-driver` | `mongostore.New(db, WithCollectionPrefix(...))` |
| `github.com/ishi-o/golem/store/redis` | `github.com/redis/go-redis` | `redisstore.New(client, WithKeyPrefix(...))` |

```go
backend, err := sqlxstore.New(db, sqlxstore.WithDialect(sqlxstore.DialectSQLite))
if err != nil { return err }
if err := backend.Migrate(ctx); err != nil { return err } // idempotent
```

The sqlx adapter covers SQL databases through dialects (SQLite is what
[golem-cli](https://github.com/ishi-o/golem-cli) uses; PostgreSQL/MySQL
work through sqlx's dialects).

## Redis is a record store here

golem's Redis backend keeps the agent's own records — pending questions,
scheduled tasks, shell credentials — not a cache. It must be provisioned
`noeviction` with AOF persistence: a Redis provisioned the usual way for
caching is free to evict a task that has not fired yet, and will do so
silently. The repository's `docker-compose.yaml` starts such an instance:

```sh
docker compose --profile redis up
docker compose --profile mongodb up
```

## Embedding

Hand the same value to both consumers:

```go
provider := tools.NewProvider(cfg, workspaces, backend, nil)
a := agent.New(chatModel, backend, provider, cfg, agent.WithBackend(backend))
```
