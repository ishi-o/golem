# Sandbox (shell tools)

The shell tools — `Bash`, `BashOutput`, `KillShell`, plus a
backend-specific restart — run commands inside a disposable per-user
environment. core owns only the tool protocol; the backends live in
`sandbox/docker` and `sandbox/kubernetes`, and core depends on neither.

```go
// core/tools
type Sandbox interface {
    Ensure(ctx context.Context, userID string) (SandboxSession, error)
    Remove(ctx context.Context, userID string) (bool, error)
    io.Closer
}
```

## The protocol

Both backends keep background-command bookkeeping **inside** the sandbox
(`/tmp/.bg/<id>.{cmd,out,pid,offset}`), so `BashOutput` reads only new
output since the last check and `KillShell` kills the process group — the
model sees one shell whichever backend a deployment picked. Idle sandboxes
are reaped by a watchdog inside the container/pod (30 minutes idle by
default, a 4-hour hard deadline). Credentials are injected at sandbox
creation from the credential store; the restart tool recreates the sandbox
so new credentials take effect.

## Docker backend

One container per user, created lazily on the first `Bash`, the user's
workspace bind-mounted at the same path on both sides. Containers belong to
the process: `Close` removes them.

```go
import "github.com/ishi-o/golem/sandbox/docker"

sandbox, err := docker.New(docker.Config{
    Image:       "your-shell-runner-image", // see sandbox/docker/shell-runner/
    Network:     "",                        // optional
    Workspaces:  workspaces,                // *storage.WorkspaceFactory
    Credentials: tools.CredentialsFromRepository(backend.ShellCredentials()),
})
```

If `Config.Client` is nil the standard Docker environment applies
(`DOCKER_HOST`, TLS variables). A runner image Dockerfile lives at
`sandbox/docker/shell-runner/`.

## Kubernetes backend

One batch Job per user (name hashed from the user id), re-attached by label
across application restarts; `Close` is a no-op — the cluster owns Job
lifetime. Credentials are per-user Secrets, consumed both as environment
variables and as a read-only mounted volume that refreshes without a
restart.

```go
import "github.com/ishi-o/golem/sandbox/kubernetes"

sandbox, err := kubernetes.New(kubernetes.Config{
    Namespace:  "default",
    Image:      "your-shell-runner-image",
    WorkingDir: "/workspace",       // absolute; the PVC mount path
    PVCMounts:  []kubernetes.PVCMount{{PVCName: "user-workspaces"}},
    Credentials: tools.CredentialsFromRepository(backend.ShellCredentials()),
})
```

Without `Config.Client` the backend uses in-cluster config, then
`KUBECONFIG`/`~/.kube/config`.

## Registration

```go
err := tools.RegisterBuiltins(provider, tools.Builtins{
    Sandbox:       sandbox,
    SandboxConfig: docker.DefaultToolsConfig(), // or kubernetes.DefaultToolsConfig()
})
```

The tools' credential family (`SetCredential`, `ListCredentials`,
`DeleteCredential`) joins automatically; values are never returned to the
model.

## In the shipped binaries

The out-of-the-box [CLI](cli.md) and [HTTP service](http-service.md) build
the backend from the environment — `GOLEM_SANDBOX=docker` or `kubernetes`
(plus the `GOLEM_SANDBOX_*` variables in the
[configuration reference](configuration.md)); unset means no shell tools.
