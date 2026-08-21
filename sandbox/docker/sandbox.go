// Package docker runs the agent shell in one Docker container per user.
// Persistent user files are bind-mounted from the application's workspace;
// command processes and credentials remain inside the container.
package docker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

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

// DefaultToolsConfig gives the Docker backend the same restart tool name as
// the reference shell integration. The persistence repository is filled by
// Provider.RegisterSandbox when it is available.
func DefaultToolsConfig() golemtools.SandboxToolsConfig {
	return golemtools.SandboxToolsConfig{RestartToolName: golemtools.ToolNameRestartShellContainer}
}

// RegisterTools adds the Docker shell tools to a core provider. The provider
// remains responsible for composition and scenario filtering.
func (s *Sandbox) RegisterTools(provider *golemtools.Provider, config golemtools.SandboxToolsConfig) error {
	if config.RestartToolName == "" {
		config.RestartToolName = golemtools.ToolNameRestartShellContainer
	}
	if s.config.Credentials == nil && config.Credentials != nil {
		s.config.Credentials = golemtools.CredentialsFromRepository(config.Credentials)
	}
	if s.config.Credentials == nil && provider != nil && provider.Repos != nil {
		s.config.Credentials = golemtools.CredentialsFromRepository(provider.Repos.ShellCredentials())
	}
	if config.Credentials == nil && provider != nil && provider.Repos != nil {
		config.Credentials = provider.Repos.ShellCredentials()
	}
	return provider.RegisterSandbox(s, config)
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
		client, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
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
	existing, err := s.client.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", LabelSandbox+"=true"), filters.Arg("label", LabelOwner+"="+userKey(userID))),
	})
	if err != nil {
		return nil, fmt.Errorf("list sandbox containers: %w", err)
	}
	if len(existing) > 0 {
		id := existing[0].ID
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
	created, err := s.client.ContainerCreate(startCtx, containerConfig, hostConfig, &network.NetworkingConfig{}, nil, name)
	if err != nil {
		return nil, fmt.Errorf("create sandbox container: %w", err)
	}
	if err := s.client.ContainerStart(startCtx, created.ID, container.StartOptions{}); err != nil {
		_ = s.client.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
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
		containers, err := s.client.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", LabelSandbox+"=true"), filters.Arg("label", LabelOwner+"="+userKey(userID)))})
		if err != nil {
			return false, fmt.Errorf("find sandbox container: %w", err)
		}
		if len(containers) == 0 {
			return false, nil
		}
		id = containers[0].ID
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

type session struct {
	sandbox *Sandbox
	id      string
}

func (s session) Exec(ctx context.Context, script string, stdin []byte, maxOutputBytes int) (golemtools.SandboxExecResult, error) {
	if maxOutputBytes <= 0 {
		maxOutputBytes = 30_000
	}
	created, err := s.sandbox.client.ContainerExecCreate(ctx, s.id, container.ExecOptions{Cmd: []string{"bash", "-c", script}, AttachStdin: len(stdin) > 0, AttachStdout: true, AttachStderr: true, Tty: false})
	if err != nil {
		return golemtools.SandboxExecResult{}, fmt.Errorf("create container exec: %w", err)
	}
	attached, err := s.sandbox.client.ContainerExecAttach(ctx, created.ID, container.ExecStartOptions{Tty: false})
	if err != nil {
		return golemtools.SandboxExecResult{}, fmt.Errorf("attach container exec: %w", err)
	}
	defer attached.Close()
	stdout := &boundedBuffer{max: maxOutputBytes}
	stderr := &boundedBuffer{max: maxOutputBytes}
	readDone := make(chan error, 1)
	go func() {
		if len(stdin) > 0 {
			if _, writeErr := attached.Conn.Write(stdin); writeErr != nil {
				readDone <- writeErr
				return
			}
			if closer, ok := attached.Conn.(interface{ CloseWrite() error }); ok {
				_ = closer.CloseWrite()
			}
		}
		_, copyErr := stdcopy.StdCopy(stdout, stderr, attached.Reader)
		readDone <- copyErr
	}()
	select {
	case <-ctx.Done():
		attached.Close()
		<-readDone
		return golemtools.SandboxExecResult{}, ctx.Err()
	case copyErr := <-readDone:
		if copyErr != nil && !errors.Is(copyErr, io.EOF) {
			return golemtools.SandboxExecResult{}, fmt.Errorf("read container exec: %w", copyErr)
		}
	}
	inspect, err := s.sandbox.client.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return golemtools.SandboxExecResult{}, fmt.Errorf("inspect container exec: %w", err)
	}
	return golemtools.SandboxExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: inspect.ExitCode, ExitCodeSet: true, Truncated: stdout.truncated || stderr.truncated}, nil
}

type boundedBuffer struct {
	data      []byte
	max       int
	truncated bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	remaining := b.max - len(b.data)
	if remaining <= 0 {
		b.truncated = true
		return len(value), nil
	}
	if len(value) > remaining {
		b.data = append(b.data, value[:remaining]...)
		b.truncated = true
		return len(value), nil
	}
	b.data = append(b.data, value...)
	return len(value), nil
}

func (b *boundedBuffer) String() string { return string(b.data) }

func (s *Sandbox) writeCredentialFiles(ctx context.Context, containerID string, credentials map[string]string) error {
	if len(credentials) == 0 {
		return nil
	}
	var script strings.Builder
	script.WriteString("set -eu\numask 077\nmkdir -p -- ")
	script.WriteString(shellQuote(s.config.CredentialsMountPath))
	script.WriteByte('\n')
	for name := range credentials {
		if !golemtools.IsValidCredentialName(name) {
			continue
		}
		fmt.Fprintf(&script, "printf '%%s' \"$%s\" > %s/%s\n", name, shellQuote(s.config.CredentialsMountPath), shellQuote(name))
	}
	fmt.Fprintf(&script, "chmod 400 %s/* 2>/dev/null || true\n", shellQuote(s.config.CredentialsMountPath))
	_, err := (session{sandbox: s, id: containerID}).Exec(ctx, script.String(), nil, 4096)
	if err != nil {
		return fmt.Errorf("write sandbox credential files: %w", err)
	}
	return nil
}

func (s *Sandbox) credentials(ctx context.Context, userID string) (map[string]string, error) {
	if s.config.Credentials == nil {
		return nil, nil
	}
	values, err := s.config.Credentials(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox credentials: %w", err)
	}
	return values, nil
}

func credentialEnvironment(credentials map[string]string) []string {
	result := make([]string, 0, len(credentials))
	for name, value := range credentials {
		if golemtools.IsValidCredentialName(name) {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func (s *Sandbox) removeContainer(ctx context.Context, id string) error {
	if err := s.client.ContainerStop(ctx, id, container.StopOptions{}); err != nil && !isNoSuchObject(err) {
		return fmt.Errorf("stop sandbox container: %w", err)
	}
	if err := s.client.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !isNoSuchObject(err) {
		return fmt.Errorf("remove sandbox container: %w", err)
	}
	return nil
}

func (s *Sandbox) isRunning(ctx context.Context, id string) (bool, error) {
	info, err := s.client.ContainerInspect(ctx, id)
	if err != nil {
		return false, err
	}
	return info.State != nil && info.State.Running, nil
}

func (s *Sandbox) lockFor(userID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lock := s.locks[userID]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	s.locks[userID] = lock
	return lock
}

func (s *Sandbox) containerFor(userID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.containers[userID]
}

func (s *Sandbox) remember(userID, id string) {
	s.mu.Lock()
	s.containers[userID] = id
	s.mu.Unlock()
}

func (s *Sandbox) forget(userID, id string) {
	s.mu.Lock()
	if s.containers[userID] == id {
		delete(s.containers, userID)
	}
	s.mu.Unlock()
}

func containerName(userID string) string {
	var random [4]byte
	_, _ = rand.Read(random[:])
	return "golem-shell-" + userKey(userID) + "-" + hex.EncodeToString(random[:])
}

func userKey(userID string) string {
	digest := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(digest[:])[:16]
}

func watchdogScript(idle, deadline time.Duration) string {
	return strings.Join([]string{
		"set -e",
		"mkdir -p /tmp/.bg",
		"touch /tmp/.last_activity",
		"START=$(date +%s)",
		"while sleep 30; do",
		"  NOW=$(date +%s)",
		"  AGE=$(( NOW - $(stat -c %Y /tmp/.last_activity) ))",
		"  if [ \"$AGE\" -gt \"$IDLE_TTL_SECONDS\" ]; then echo \"shell sandbox idle for ${AGE}s, exiting\"; exit 0; fi",
		"  if [ \"$(( NOW - START ))\" -gt \"$MAX_LIFETIME_SECONDS\" ]; then echo \"shell sandbox reached its hard deadline, exiting\"; exit 0; fi",
		"done",
	}, "\n") + "\n"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func parseCPU(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if strings.HasSuffix(value, "m") {
		milli, err := strconv.ParseFloat(strings.TrimSuffix(value, "m"), 64)
		if err != nil || milli <= 0 {
			return 0, fmt.Errorf("invalid cpu limit %q", value)
		}
		return int64(math.Round(milli * 1_000_000)), nil
	}
	cpus, err := strconv.ParseFloat(value, 64)
	if err != nil || cpus <= 0 {
		return 0, fmt.Errorf("invalid cpu limit %q", value)
	}
	return int64(math.Round(cpus * 1_000_000_000)), nil
}

func parseMemory(value string) (int64, error) {
	value = strings.TrimSpace(value)
	suffixes := []struct {
		suffix string
		factor int64
	}{
		{"Gi", 1024 * 1024 * 1024}, {"Mi", 1024 * 1024}, {"Ki", 1024},
		{"Ti", 1024 * 1024 * 1024 * 1024}, {"G", 1_000_000_000}, {"M", 1_000_000}, {"K", 1_000},
	}
	for _, item := range suffixes {
		if strings.HasSuffix(value, item.suffix) {
			number, err := strconv.ParseFloat(strings.TrimSuffix(value, item.suffix), 64)
			if err != nil || number <= 0 {
				return 0, fmt.Errorf("invalid memory limit %q", value)
			}
			return int64(math.Round(number * float64(item.factor))), nil
		}
	}
	bytes, err := strconv.ParseInt(value, 10, 64)
	if err != nil || bytes <= 0 {
		return 0, fmt.Errorf("invalid memory limit %q", value)
	}
	return bytes, nil
}

func mustInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func isNoSuchObject(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such container")
}

var _ golemtools.Sandbox = (*Sandbox)(nil)
