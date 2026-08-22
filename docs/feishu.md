# Feishu connector

`connector/feishu` is the optional Feishu/Lark surface: it receives Feishu
event callbacks, turns message events into agent runs, streams the replies
back as Feishu messages, hosts interactive question cards, and protects
against duplicate deliveries. It is the reference implementation of a
connector (see [Extending](extending.md#connectors)) and depends only on
core plus the official
[oapi-sdk-go/v3](https://github.com/larksuite/oapi-sdk-go) SDK.

```sh
go get github.com/ishi-o/golem/connector/feishu
```

No environment variables are read — the app id, secret and webhook token
come from your own configuration.

## Client

```go
client, err := feishu.NewClient(feishu.ClientConfig{
    AppID:     appID,     // required
    AppSecret: appSecret, // required
    BaseURL:   "",        // optional; Feishu default (Lark: the Lark domain)
    Logger:    logger,
})
```

The client wraps the official SDK, which owns tenant-token caching; the
small messaging surface (send text/cards by `ReceiveIDType` — open id,
user id, union id, email, chat id) is what the handler uses to reply.

## Handler

```go
handler := feishu.NewHandler(agent, backend, client, logger,
    feishu.WithVerificationToken(token), // Feishu URL-verification + event tokens
    feishu.WithQuestionTTL(24*time.Hour), // how long a question card stays answerable
)
```

`NewHandler` takes the agent, the store backend (duplicate-message
protection and pending questions are records in it), the client, a logger
and options. The handler is an `http.Handler` for the event webhook:

```go
mux := http.NewServeMux()
mux.Handle("/webhooks/feishu", handler)
```

or, in this repository's [HTTP service](http-service.md), mounted by
setting `RouterConfig.FeishuHandler` — the route is
`POST /webhooks/feishu`.

## Behaviour

- **Acknowledge first.** The webhook is acknowledged immediately; model
  work and replies continue asynchronously. Feishu retries deliveries it
  does not see acknowledged, and a retry must not start a duplicate run.
- **Duplicate protection.** Message ids are recorded in the backend's
  processed-message store; a redelivered event is dropped, not re-run.
- **Questions.** The ask tool surfaces as interactive cards; answers arrive
  as card actions and resume the run (subject to the question TTL — the
  same `GOLEM_ASK_USER_TTL` idea, passed programmatically here via
  `WithQuestionTTL`).
- **Identity.** Feishu open ids become the run's user and chat identity, so
  per-user workspaces, memory and scheduled tasks scope correctly.

The connector keeps no state of its own beyond the clock: everything
durable lives in the store you pass it.
