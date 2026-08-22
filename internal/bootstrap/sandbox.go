package bootstrap

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/store"
	"github.com/ishi-o/golem/core/tools"
	"github.com/ishi-o/golem/sandbox/docker"
	"github.com/ishi-o/golem/sandbox/kubernetes"
)

const (
	sandboxEnv         = "GOLEM_SANDBOX" // "docker", "kubernetes", or unset (off)
	sandboxImageEnv    = "GOLEM_SANDBOX_IMAGE"
	sandboxNetworkEnv  = "GOLEM_SANDBOX_NETWORK"           // docker only
	sandboxNamespaceEnv = "GOLEM_SANDBOX_NAMESPACE"        // kubernetes only
	sandboxWorkingDirEnv = "GOLEM_SANDBOX_WORKING_DIR"     // kubernetes only
	sandboxPVCEnv      = "GOLEM_SANDBOX_PVC"               // kubernetes only
	sandboxPVCSubpathEnv = "GOLEM_SANDBOX_PVC_SUBPATH"     // kubernetes only
)

// newSandbox builds the sandbox backend GOLEM_SANDBOX selects, wired to the
// user workspaces and the credential store. A backend that is off (unset)
// returns nils — no shell tools are offered, exactly as before the variable
// existed. Misconfiguration (a backend selected without its required
// variables) is a startup error, not a silent no-op.
func newSandbox(backend store.Backend, workspaces *storage.WorkspaceFactory, logger *slog.Logger) (tools.Sandbox, tools.SandboxToolsConfig, error) {
	kind := strings.TrimSpace(os.Getenv(sandboxEnv))
	image := strings.TrimSpace(os.Getenv(sandboxImageEnv))
	credentials := tools.CredentialsFromRepository(backend.ShellCredentials())
	switch kind {
	case "":
		return nil, tools.SandboxToolsConfig{}, nil
	case "docker":
		if image == "" {
			return nil, tools.SandboxToolsConfig{}, fmt.Errorf("bootstrap: %s is required when GOLEM_SANDBOX=docker", sandboxImageEnv)
		}
		sandbox, err := docker.New(docker.Config{
			Image:       image,
			Network:     strings.TrimSpace(os.Getenv(sandboxNetworkEnv)),
			Workspaces:  workspaces,
			Credentials: credentials,
			Logger:      logger,
		})
		if err != nil {
			return nil, tools.SandboxToolsConfig{}, fmt.Errorf("bootstrap: docker sandbox: %w", err)
		}
		return sandbox, docker.DefaultToolsConfig(), nil
	case "kubernetes":
		workingDir := strings.TrimSpace(os.Getenv(sandboxWorkingDirEnv))
		pvc := strings.TrimSpace(os.Getenv(sandboxPVCEnv))
		if image == "" || workingDir == "" || pvc == "" {
			return nil, tools.SandboxToolsConfig{}, fmt.Errorf("bootstrap: %s, %s and %s are required when GOLEM_SANDBOX=kubernetes",
				sandboxImageEnv, sandboxWorkingDirEnv, sandboxPVCEnv)
		}
		sandbox, err := kubernetes.New(kubernetes.Config{
			Namespace:  strings.TrimSpace(os.Getenv(sandboxNamespaceEnv)),
			Image:      image,
			WorkingDir: workingDir,
			PVCMounts: []kubernetes.PVCMount{{
				PVCName:       pvc,
				SubPathPrefix: strings.TrimSpace(os.Getenv(sandboxPVCSubpathEnv)),
			}},
			Credentials: credentials,
			Logger:      logger,
		})
		if err != nil {
			return nil, tools.SandboxToolsConfig{}, fmt.Errorf("bootstrap: kubernetes sandbox: %w", err)
		}
		return sandbox, kubernetes.DefaultToolsConfig(), nil
	default:
		return nil, tools.SandboxToolsConfig{}, fmt.Errorf("bootstrap: %s=%q is not docker or kubernetes", sandboxEnv, kind)
	}
}
