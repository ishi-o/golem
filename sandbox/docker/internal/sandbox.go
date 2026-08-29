// Package docker runs the agent shell in one Docker container per user.
// Persistent user files are bind-mounted from the application's workspace;
// command processes and credentials remain inside the container.
package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/ishi-o/golem/core/storage"
	golemtools "github.com/ishi-o/golem/core/tools"
)

const (
	LabelSandbox  = "golem.io/sandbox"
	LabelOwner    = "golem.io/owner"
	LabelRole     = "golem.io/role"
	LabelRoleUser = "user"

	defaultCredentialsMountPath = "/run/secrets/credentials"
)

// Config controls the Docker sandbox. Image and Workspaces are required.
// Credentials are resolved only while a new container is created.
type Config struct {
	Client     *client.Client
	Logger     *slog.Logger
	Image      string
	Network    string
	Workspaces *storage.WorkspaceFactory

	Credentials          golemtools.CredentialResolver
	CredentialsMountPath string

	IdleTimeout    time.Duration
	HardDeadline   time.Duration
	StartupTimeout time.Duration
	CPULimit       string
	MemoryLimit    string
}

// DefaultToolsConfig gives the Docker backend its restart tool name.
func DefaultToolsConfig() golemtools.SandboxToolsConfig {
	return golemtools.SandboxToolsConfig{RestartToolName: golemtools.ToolNameRestartShellContainer}
}

// Sandbox implements tools.Sandbox with the Docker Engine API.
type Sandbox struct {
	client *client.Client
	config Config
	log    *slog.Logger

	mu         sync.Mutex
	containers map[string]string
	locks      map[string]*sync.Mutex
}

// New creates a Docker sandbox manager. When Client is nil, the standard
// Docker environment variables and socket are used.
func New(config Config) (*Sandbox, error) {
	if strings.TrimSpace(config.Image) == "" {
		return nil, errors.New("docker sandbox: image is required")
	}
	if config.Workspaces == nil {
		return nil, errors.New("docker sandbox: workspaces are required")
	}
	if config.Client == nil {
		client, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return nil, fmt.Errorf("docker sandbox: create client: %w", err)
		}
		config.Client = client
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.CredentialsMountPath == "" {
		config.CredentialsMountPath = defaultCredentialsMountPath
	}
	if !filepath.IsAbs(config.CredentialsMountPath) {
		return nil, fmt.Errorf("docker sandbox: credentials mount path must be absolute")
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = 30 * time.Minute
	}
	if config.HardDeadline <= 0 {
		config.HardDeadline = 4 * time.Hour
	}
	if config.StartupTimeout <= 0 {
		config.StartupTimeout = time.Minute
	}
	if config.CPULimit == "" {
		config.CPULimit = "1000m"
	}
	if config.MemoryLimit == "" {
		config.MemoryLimit = "1Gi"
	}
	nanoCPUs, err := parseCPU(config.CPULimit)
	if err != nil {
		return nil, fmt.Errorf("docker sandbox: %w", err)
	}
	memory, err := parseMemory(config.MemoryLimit)
	if err != nil {
		return nil, fmt.Errorf("docker sandbox: %w", err)
	}
	config.CPULimit = strconv.FormatInt(nanoCPUs, 10)
	config.MemoryLimit = strconv.FormatInt(memory, 10)
	return &Sandbox{client: config.Client, config: config, log: config.Logger, containers: map[string]string{}, locks: map[string]*sync.Mutex{}}, nil
}

// Ensure returns a running container for userID, creating one on first use.
func (s *Sandbox) Ensure(ctx context.Context, userID string) (golemtools.SandboxSession, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("docker sandbox: user id is required")
	}
	lock := s.lockFor(userID)
	lock.Lock()
	defer lock.Unlock()

	if id := s.containerFor(userID); id != "" {
		if running, err := s.isRunning(ctx, id); err == nil && running {
			return session{sandbox: s, id: id}, nil
		}
		_ = s.removeContainer(context.Background(), id)
		s.forget(userID, id)
	}

	// A process restart should not create a second sandbox while an earlier
	// one is still alive. Labels are the durable registry; the in-memory map is
	// only the fast path.
	existing, err := s.client.ContainerList(ctx, client.ContainerListOptions{
		Filters: client.Filters{}.Add("label", LabelSandbox+"=true", LabelOwner+"="+userKey(userID)),
	})
	if err != nil {
		return nil, fmt.Errorf("list sandbox containers: %w", err)
	}
	if len(existing.Items) > 0 {
		id := existing.Items[0].ID
		s.remember(userID, id)
		return session{sandbox: s, id: id}, nil
	}

	home := s.config.Workspaces.ForOwner(userID).Root()
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, fmt.Errorf("create user workspace: %w", err)
	}
	credentials, err := s.credentials(ctx, userID)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{LabelSandbox: "true", LabelOwner: userKey(userID), LabelRole: LabelRoleUser}
	containerConfig := &container.Config{
		Image:      s.config.Image,
		WorkingDir: home,
		Env: append(credentialEnvironment(credentials),
			"IDLE_TTL_SECONDS="+strconv.FormatInt(int64(s.config.IdleTimeout/time.Second), 10),
			"MAX_LIFETIME_SECONDS="+strconv.FormatInt(int64(s.config.HardDeadline/time.Second), 10),
		),
		Labels: labels,
		Cmd:    []string{"sh", "-c", watchdogScript(s.config.IdleTimeout, s.config.HardDeadline)},
	}
	hostConfig := &container.HostConfig{
		Binds: []string{home + ":" + home},
		Resources: container.Resources{
			NanoCPUs: mustInt64(s.config.CPULimit),
			Memory:   mustInt64(s.config.MemoryLimit),
		},
		Mounts: []mount.Mount{{Type: mount.TypeTmpfs, Target: s.config.CredentialsMountPath, TmpfsOptions: &mount.TmpfsOptions{Options: [][]string{{"rw"}, {"noexec"}, {"nosuid"}, {"size", "1m"}}}}},
	}
	if s.config.Network != "" {
		hostConfig.NetworkMode = container.NetworkMode(s.config.Network)
	}
	startCtx, cancel := context.WithTimeout(ctx, s.config.StartupTimeout)
	defer cancel()
	name := containerName(userID)
	created, err := s.client.ContainerCreate(startCtx, client.ContainerCreateOptions{
		Config:           containerConfig,
		HostConfig:       hostConfig,
		NetworkingConfig: &network.NetworkingConfig{},
		Name:             name,
	})
	if err != nil {
		return nil, fmt.Errorf("create sandbox container: %w", err)
	}
	if _, err := s.client.ContainerStart(startCtx, created.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = s.client.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		return nil, fmt.Errorf("start sandbox container: %w", err)
	}
	s.remember(userID, created.ID)
	s.log.Info("created Docker sandbox", "user_id", userID, "container_id", created.ID)
	if err := s.writeCredentialFiles(startCtx, created.ID, credentials); err != nil {
		_ = s.removeContainer(context.Background(), created.ID)
		s.forget(userID, created.ID)
		return nil, err
	}
	return session{sandbox: s, id: created.ID}, nil
}

// Remove stops and removes the user's container. Persistent files are not
// touched because they live outside the container layer.
func (s *Sandbox) Remove(ctx context.Context, userID string) (bool, error) {
	if strings.TrimSpace(userID) == "" {
		return false, errors.New("docker sandbox: user id is required")
	}
	lock := s.lockFor(userID)
	lock.Lock()
	defer lock.Unlock()
	id := s.containerFor(userID)
	if id == "" {
		containers, err := s.client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", LabelSandbox+"=true", LabelOwner+"="+userKey(userID))})
		if err != nil {
			return false, fmt.Errorf("find sandbox container: %w", err)
		}
		if len(containers.Items) == 0 {
			return false, nil
		}
		id = containers.Items[0].ID
	}
	if err := s.removeContainer(ctx, id); err != nil {
		return false, err
	}
	s.forget(userID, id)
	s.log.Info("removed Docker sandbox", "user_id", userID, "container_id", id)
	return true, nil
}

// Close removes containers created or adopted by this manager. Kubernetes
// deliberately has different lifetime semantics; Docker containers belong
// to this process and should not be left behind on shutdown.
func (s *Sandbox) Close() error {
	s.mu.Lock()
	ids := make([]string, 0, len(s.containers))
	for _, id := range s.containers {
		ids = append(ids, id)
	}
	s.containers = map[string]string{}
	s.mu.Unlock()
	var errs []error
	for _, id := range ids {
		if err := s.removeContainer(context.Background(), id); err != nil {
			errs = append(errs, err)
		}
	}
	s.log.Info("closed Docker sandboxes", "count", len(ids))
	return errors.Join(errs...)
}
