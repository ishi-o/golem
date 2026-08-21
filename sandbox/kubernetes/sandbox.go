// Package kubernetes runs the agent shell in one Kubernetes Job/Pod per user.
// Persistent files live on caller-provided PVC subpaths; command processes and
// credentials live in the disposable Pod.
package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	golemtools "github.com/ishi-o/golem/core/tools"
)

const (
	LabelSandbox  = "golem.io/sandbox"
	LabelOwner    = "golem.io/owner"
	LabelRole     = "golem.io/role"
	LabelRoleUser = "user"

	ContainerName       = "shell"
	CredentialsVolume   = "user-credentials"
	credentialSecretTag = "golem.io/credentials"

	defaultCredentialsMountPath = "/run/secrets/credentials"
	defaultJobTTL               = 60 * time.Second
	maxCredentialBytes          = 900 * 1024
)

// PVCMount describes one persistent volume exposed to every user sandbox.
// The final subpath is SubPathPrefix/userKey, so users cannot see one another's
// files when they share a PVC.
type PVCMount struct {
	PVCName       string
	MountPath     string
	SubPathPrefix string
}

// Config controls the Kubernetes sandbox. Image, WorkingDir, RESTConfig and
// at least one PVC mount are required. Credentials are resolved only while a
// new Job is being created and are stored in a per-user Secret.
type Config struct {
	Client     kubeclient.Interface
	RESTConfig *rest.Config
	Logger     *slog.Logger
	Namespace  string
	Image      string
	WorkingDir string

	PVCMounts        []PVCMount
	ImagePullSecrets []string
	PodAnnotations   map[string]string
	FSGroup          *int64

	Credentials          golemtools.CredentialResolver
	CredentialsMountPath string

	IdleTimeout    time.Duration
	HardDeadline   time.Duration
	StartupTimeout time.Duration

	CPURequest    string
	MemoryRequest string
	CPULimit      string
	MemoryLimit   string

	// UserSubPath lets a deployment choose its own safe per-user directory
	// name. The default is a stable hash, which is valid for PVC subPath and
	// does not disclose a user identifier in Kubernetes metadata.
	UserSubPath func(userID string) string
}

// DefaultToolsConfig gives the Kubernetes backend the same restart tool name
// as the reference shell integration. The persistence repository is filled by
// Provider.RegisterSandbox when it is available.
func DefaultToolsConfig() golemtools.SandboxToolsConfig {
	return golemtools.SandboxToolsConfig{RestartToolName: golemtools.ToolNameRestartShellPod}
}

// RegisterTools adds the Kubernetes shell tools to a core provider. The
// provider remains responsible for composition and scenario filtering.
func (s *Sandbox) RegisterTools(provider *golemtools.Provider, config golemtools.SandboxToolsConfig) error {
	if config.RestartToolName == "" {
		config.RestartToolName = golemtools.ToolNameRestartShellPod
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

// Sandbox implements tools.Sandbox with Kubernetes Jobs and pods/exec.
type Sandbox struct {
	client     kubeclient.Interface
	restConfig *rest.Config
	config     Config
	log        *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New creates a Kubernetes sandbox manager. If Client and RESTConfig are nil,
// it first tries in-cluster configuration and then the default kubeconfig.
func New(config Config) (*Sandbox, error) {
	if strings.TrimSpace(config.Image) == "" {
		return nil, errors.New("kubernetes sandbox: image is required")
	}
	if !path.IsAbs(config.WorkingDir) {
		return nil, errors.New("kubernetes sandbox: working directory must be absolute")
	}
	if len(config.PVCMounts) == 0 {
		return nil, errors.New("kubernetes sandbox: at least one PVC mount is required")
	}
	for i := range config.PVCMounts {
		mount := &config.PVCMounts[i]
		if strings.TrimSpace(mount.PVCName) == "" {
			return nil, fmt.Errorf("kubernetes sandbox: PVC mount %d has no PVC name", i)
		}
		if mount.MountPath == "" {
			mount.MountPath = "/" + mount.PVCName
		}
		if !path.IsAbs(mount.MountPath) {
			return nil, fmt.Errorf("kubernetes sandbox: PVC mount %q path must be absolute", mount.PVCName)
		}
		if mount.SubPathPrefix != "" && !validSubPath(mount.SubPathPrefix) {
			return nil, fmt.Errorf("kubernetes sandbox: invalid subpath prefix %q", mount.SubPathPrefix)
		}
	}
	if config.Namespace == "" {
		config.Namespace = "default"
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.CredentialsMountPath == "" {
		config.CredentialsMountPath = defaultCredentialsMountPath
	}
	if !path.IsAbs(config.CredentialsMountPath) {
		return nil, errors.New("kubernetes sandbox: credentials mount path must be absolute")
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
	if config.CPURequest == "" {
		config.CPURequest = "100m"
	}
	if config.MemoryRequest == "" {
		config.MemoryRequest = "256Mi"
	}
	if config.CPULimit == "" {
		config.CPULimit = "1000m"
	}
	if config.MemoryLimit == "" {
		config.MemoryLimit = "1Gi"
	}
	for name, value := range map[string]string{
		"cpu request": config.CPURequest, "memory request": config.MemoryRequest,
		"cpu limit": config.CPULimit, "memory limit": config.MemoryLimit,
	} {
		quantity, err := resource.ParseQuantity(value)
		if err != nil || quantity.Sign() <= 0 {
			return nil, fmt.Errorf("kubernetes sandbox: invalid %s %q", name, value)
		}
	}
	if config.PodAnnotations == nil {
		config.PodAnnotations = map[string]string{}
	}
	if _, ok := config.PodAnnotations["sidecar.istio.io/inject"]; !ok {
		config.PodAnnotations["sidecar.istio.io/inject"] = "false"
	}
	secrets := make([]string, 0, len(config.ImagePullSecrets))
	for _, name := range config.ImagePullSecrets {
		if name = strings.TrimSpace(name); name != "" {
			secrets = append(secrets, name)
		}
	}
	config.ImagePullSecrets = secrets

	if config.Client == nil {
		restConfig, err := loadRESTConfig()
		if err != nil {
			return nil, fmt.Errorf("kubernetes sandbox: load client config: %w", err)
		}
		config.RESTConfig = restConfig
		config.Client, err = kubeclient.NewForConfig(restConfig)
		if err != nil {
			return nil, fmt.Errorf("kubernetes sandbox: create client: %w", err)
		}
	} else if config.RESTConfig == nil {
		return nil, errors.New("kubernetes sandbox: RESTConfig is required with a supplied Client")
	}

	return &Sandbox{
		client:     config.Client,
		restConfig: config.RESTConfig,
		config:     config,
		log:        config.Logger,
		locks:      map[string]*sync.Mutex{},
	}, nil
}

// Ensure returns a running or starting shell Pod for userID, creating a Job
// on first use. The per-user lock prevents duplicate Jobs in this process.
func (s *Sandbox) Ensure(ctx context.Context, userID string) (golemtools.SandboxSession, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("kubernetes sandbox: user id is required")
	}
	lock := s.lockFor(userID)
	lock.Lock()
	defer lock.Unlock()

	if pod, err := s.findPod(ctx, userID, ""); err != nil {
		return nil, fmt.Errorf("find shell pod: %w", err)
	} else if pod != nil {
		if pod.Status.Phase == corev1.PodRunning {
			return session{sandbox: s, pod: pod.Name}, nil
		}
		if jobName := pod.Labels["batch.kubernetes.io/job-name"]; jobName != "" {
			pod, err = s.waitForPod(ctx, userID, jobName)
			if err != nil {
				return nil, err
			}
			return session{sandbox: s, pod: pod.Name}, nil
		}
	}

	secretName := ""
	if s.config.Credentials != nil {
		credentials, err := s.config.Credentials(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("resolve sandbox credentials: %w", err)
		}
		secretName, err = s.ensureCredentialsSecret(ctx, userID, credentials)
		if err != nil {
			return nil, err
		}
	}
	job, err := s.client.BatchV1().Jobs(s.config.Namespace).Create(ctx, s.buildJob(userID, secretName), metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create shell job: %w", err)
	}
	s.log.Info("created Kubernetes sandbox", "user_id", userID, "job", job.Name)
	pod, err := s.waitForPod(ctx, userID, job.Name)
	if err != nil {
		return nil, err
	}
	return session{sandbox: s, pod: pod.Name}, nil
}

// Remove deletes all Jobs belonging to the user and their Pods. Persistent
// PVC files are intentionally left untouched; the next Bash call creates a
// fresh disposable sandbox. The credential Secret is removed as well.
func (s *Sandbox) Remove(ctx context.Context, userID string) (bool, error) {
	if strings.TrimSpace(userID) == "" {
		return false, errors.New("kubernetes sandbox: user id is required")
	}
	lock := s.lockFor(userID)
	lock.Lock()
	defer lock.Unlock()

	selector := ownerSelector(userID)
	jobs, err := s.client.BatchV1().Jobs(s.config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return false, fmt.Errorf("list shell jobs: %w", err)
	}
	removed := false
	var errs []error
	for _, job := range jobs.Items {
		propagation := metav1.DeletePropagationBackground
		if err := s.client.BatchV1().Jobs(s.config.Namespace).Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("delete shell job %s: %w", job.Name, err))
			continue
		}
		removed = true
	}
	if s.config.Credentials != nil {
		if err := s.client.CoreV1().Secrets(s.config.Namespace).Delete(ctx, credentialSecretName(userID), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("delete shell credentials: %w", err))
		} else if !apierrors.IsNotFound(err) {
			removed = true
		}
	}
	if removed {
		s.log.Info("removed Kubernetes sandbox", "user_id", userID, "jobs", len(jobs.Items))
	}
	return removed, errors.Join(errs...)
}

// Close is intentionally a no-op. Kubernetes owns Job/Pod lifetime, so an
// application shutdown must not delete another process's running sandbox.
func (s *Sandbox) Close() error { return nil }
