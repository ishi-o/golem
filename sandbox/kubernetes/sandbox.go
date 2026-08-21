// Package kubernetes runs the agent shell in one Kubernetes Job/Pod per user.
// Persistent files live on caller-provided PVC subpaths; command processes and
// credentials live in the disposable Pod.
package kubernetes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"

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

type session struct {
	sandbox *Sandbox
	pod     string
}

const execExitMarker = "__GOLEM_EXEC_EXIT__"

func (s session) Exec(ctx context.Context, script string, stdin []byte, maxOutputBytes int) (golemtools.SandboxExecResult, error) {
	if maxOutputBytes <= 0 {
		maxOutputBytes = 30_000
	}
	wrapper := strings.Join([]string{
		"set +e",
		"(",
		script,
		")",
		"code=$?",
		"printf '\\n" + execExitMarker + ":%d\\n' \"$code\"",
		"exit 0",
	}, "\n")
	command := []string{"bash", "-c", wrapper}
	request := s.sandbox.client.CoreV1().RESTClient().Post().
		Namespace(s.sandbox.config.Namespace).
		Resource("pods").
		Name(s.pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: ContainerName,
			Command:   command,
			Stdin:     len(stdin) > 0,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(s.sandbox.restConfig, "POST", request.URL())
	if err != nil {
		return golemtools.SandboxExecResult{}, fmt.Errorf("create pod exec: %w", err)
	}
	stdout := newExecBuffer(maxOutputBytes)
	stderr := newExecBuffer(maxOutputBytes)
	streamErr := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  bytes.NewReader(stdin),
		Stdout: stdout,
		Stderr: stderr,
		Tty:    false,
	})
	if streamErr != nil {
		if ctx.Err() != nil {
			return golemtools.SandboxExecResult{}, ctx.Err()
		}
		return golemtools.SandboxExecResult{}, fmt.Errorf("stream pod exec: %w", streamErr)
	}
	output, exitCode, exitCodeSet := parseExitMarker(stdout.String())
	stdoutTruncated := stdout.Truncated()
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes]
		stdoutTruncated = true
	}
	stderrOutput := stderr.String()
	stderrTruncated := stderr.Truncated()
	if len(stderrOutput) > maxOutputBytes {
		stderrOutput = stderrOutput[:maxOutputBytes]
		stderrTruncated = true
	}
	return golemtools.SandboxExecResult{
		Stdout:      output,
		Stderr:      stderrOutput,
		ExitCode:    exitCode,
		ExitCodeSet: exitCodeSet,
		Truncated:   stdoutTruncated || stderrTruncated,
	}, nil
}

func (s *Sandbox) buildJob(userID, secretName string) *batchv1.Job {
	labelsForUser := map[string]string{LabelSandbox: "true", LabelOwner: userKey(userID), LabelRole: LabelRoleUser}
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(s.config.CPURequest),
			corev1.ResourceMemory: resource.MustParse(s.config.MemoryRequest),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(s.config.CPULimit),
			corev1.ResourceMemory: resource.MustParse(s.config.MemoryLimit),
		},
	}
	container := corev1.Container{
		Name:            ContainerName,
		Image:           s.config.Image,
		ImagePullPolicy: corev1.PullAlways,
		WorkingDir:      s.config.WorkingDir,
		Command:         []string{"sh", "-c", watchdogScript(s.config.IdleTimeout, s.config.HardDeadline)},
		Env: []corev1.EnvVar{
			{Name: "IDLE_TTL_SECONDS", Value: durationSeconds(s.config.IdleTimeout)},
			{Name: "MAX_LIFETIME_SECONDS", Value: durationSeconds(s.config.HardDeadline)},
		},
		Resources: resources,
	}
	volumes := make([]corev1.Volume, 0, len(s.config.PVCMounts)+1)
	if secretName != "" {
		optional := true
		defaultMode := int32(0o400)
		container.EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Optional: &optional}}}
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: CredentialsVolume, MountPath: s.config.CredentialsMountPath, ReadOnly: true})
		volumes = append(volumes, corev1.Volume{Name: CredentialsVolume, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretName, Optional: &optional, DefaultMode: &defaultMode}}})
	}
	for i, pvcMount := range s.config.PVCMounts {
		volumeName := fmt.Sprintf("user-mount-%d", i)
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name: volumeName, MountPath: pvcMount.MountPath, SubPath: s.subPath(userID, pvcMount), ReadOnly: false,
		})
		volumes = append(volumes, corev1.Volume{Name: volumeName, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcMount.PVCName}}})
	}
	podSpec := corev1.PodSpec{
		RestartPolicy:                 corev1.RestartPolicyNever,
		AutomountServiceAccountToken:  boolPtr(false),
		ImagePullSecrets:              imagePullSecrets(s.config.ImagePullSecrets),
		Containers:                    []corev1.Container{container},
		Volumes:                       volumes,
		TerminationGracePeriodSeconds: int64Ptr(10),
	}
	if s.config.FSGroup != nil {
		podSpec.SecurityContext = &corev1.PodSecurityContext{FSGroup: s.config.FSGroup}
	}
	annotations := make(map[string]string, len(s.config.PodAnnotations))
	for key, value := range s.config.PodAnnotations {
		annotations[key] = value
	}
	one := int32(1)
	zero := int32(0)
	ttl := int32(defaultJobTTL / time.Second)
	activeDeadline := durationSecondsInt64(s.config.HardDeadline)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "golem-shell-" + userKey(userID) + "-", Labels: labelsForUser},
		Spec: batchv1.JobSpec{
			Completions:             &one,
			Parallelism:             &one,
			BackoffLimit:            &zero,
			TTLSecondsAfterFinished: &ttl,
			ActiveDeadlineSeconds:   &activeDeadline,
			Template:                corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labelsForUser, Annotations: annotations}, Spec: podSpec},
		},
	}
}

func (s *Sandbox) waitForPod(ctx context.Context, userID, jobName string) (*corev1.Pod, error) {
	waitCtx, cancel := context.WithTimeout(ctx, s.config.StartupTimeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		pod, err := s.findPod(waitCtx, userID, jobName)
		if err != nil {
			return nil, fmt.Errorf("wait for shell pod: %w", err)
		}
		if pod != nil && pod.Status.Phase == corev1.PodRunning {
			return pod, nil
		}
		job, err := s.client.BatchV1().Jobs(s.config.Namespace).Get(waitCtx, jobName, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("inspect shell job: %w", err)
		}
		if err == nil && jobTerminated(job) {
			return nil, fmt.Errorf("shell job %s terminated before its Pod became running", jobName)
		}
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("shell pod for user %q did not become running within %s: %w", userID, s.config.StartupTimeout, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Sandbox) findPod(ctx context.Context, userID, jobName string) (*corev1.Pod, error) {
	selector := ownerSelector(userID)
	if jobName != "" {
		selector += ",batch.kubernetes.io/job-name=" + jobName
	}
	pods, err := s.client.CoreV1().Pods(s.config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	items := make([]corev1.Pod, 0, len(pods.Items))
	for _, pod := range pods.Items {
		switch pod.Status.Phase {
		case corev1.PodPending, corev1.PodRunning, corev1.PodUnknown:
			items = append(items, pod)
		case corev1.PodFailed:
			if jobName != "" {
				return nil, fmt.Errorf("shell pod %s failed: %s", pod.Name, podFailureReason(pod))
			}
		}
	}
	if len(items) == 0 {
		return nil, nil
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreationTimestamp.Before(&items[j].CreationTimestamp)
	})
	return &items[len(items)-1], nil
}

func (s *Sandbox) ensureCredentialsSecret(ctx context.Context, userID string, credentials map[string]string) (string, error) {
	data := make(map[string][]byte, len(credentials))
	total := 0
	for name, value := range credentials {
		if !golemtools.IsValidCredentialName(name) {
			continue
		}
		encoded := []byte(value)
		total += len(name) + len(encoded)
		if total > maxCredentialBytes {
			return "", fmt.Errorf("kubernetes sandbox: credentials exceed %d bytes", maxCredentialBytes)
		}
		data[name] = encoded
	}
	name := credentialSecretName(userID)
	secrets := s.client.CoreV1().Secrets(s.config.Namespace)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{LabelSandbox: "true", LabelOwner: userKey(userID), credentialSecretTag: "true"}}, Type: corev1.SecretTypeOpaque, Data: data}, metav1.CreateOptions{})
		if err != nil {
			return "", fmt.Errorf("create sandbox credentials: %w", err)
		}
		return name, nil
	}
	if err != nil {
		return "", fmt.Errorf("get sandbox credentials: %w", err)
	}
	existing.Data = data
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	existing.Labels[LabelSandbox] = "true"
	existing.Labels[LabelOwner] = userKey(userID)
	existing.Labels[credentialSecretTag] = "true"
	if _, err := secrets.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("update sandbox credentials: %w", err)
	}
	return name, nil
}

func (s *Sandbox) subPath(userID string, mount PVCMount) string {
	userPath := userKey(userID)
	if s.config.UserSubPath != nil {
		userPath = s.config.UserSubPath(userID)
	}
	if !validSubPath(userPath) {
		userPath = userKey(userID)
	}
	if mount.SubPathPrefix == "" {
		return userPath
	}
	joined := path.Join(mount.SubPathPrefix, userPath)
	if !validSubPath(joined) {
		return path.Join(mount.SubPathPrefix, userKey(userID))
	}
	return joined
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

func loadRESTConfig() (*rest.Config, error) {
	if config, err := rest.InClusterConfig(); err == nil {
		return config, nil
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

func imagePullSecrets(names []string) []corev1.LocalObjectReference {
	refs := make([]corev1.LocalObjectReference, 0, len(names))
	for _, name := range names {
		refs = append(refs, corev1.LocalObjectReference{Name: name})
	}
	return refs
}

func ownerSelector(userID string) string {
	return labels.Set{LabelSandbox: "true", LabelOwner: userKey(userID), LabelRole: LabelRoleUser}.AsSelector().String()
}

func credentialSecretName(userID string) string {
	return "golem-shell-creds-" + userKey(userID)
}

func userKey(userID string) string {
	digest := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(digest[:])[:16]
}

func validSubPath(value string) bool {
	clean := path.Clean(value)
	return value != "" && clean == value && clean != "." && clean != ".." && !path.IsAbs(value) && !strings.HasPrefix(clean, "../")
}

func jobTerminated(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	if job.Status.Succeeded > 0 || job.Status.Failed > 0 {
		return true
	}
	for _, condition := range job.Status.Conditions {
		if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func podFailureReason(pod corev1.Pod) string {
	if pod.Status.Reason != "" {
		return pod.Status.Reason
	}
	if pod.Status.Message != "" {
		return pod.Status.Message
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Terminated != nil && status.State.Terminated.Reason != "" {
			return status.State.Terminated.Reason
		}
		if status.State.Waiting != nil && status.State.Waiting.Reason != "" {
			return status.State.Waiting.Reason
		}
	}
	return "unknown failure"
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

func durationSeconds(value time.Duration) string {
	return fmt.Sprintf("%d", durationSecondsInt64(value))
}

func durationSecondsInt64(value time.Duration) int64 {
	seconds := int64(value / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

func boolPtr(value bool) *bool    { return &value }
func int64Ptr(value int64) *int64 { return &value }

type execBuffer struct {
	max       int
	head      []byte
	tail      []byte
	truncated bool
}

func newExecBuffer(max int) *execBuffer {
	return &execBuffer{max: max, head: make([]byte, 0, max), tail: make([]byte, 0, len(execExitMarker)+32)}
}

func (b *execBuffer) Write(value []byte) (int, error) {
	written := len(value)
	if len(b.head) < b.max {
		count := minInt(b.max-len(b.head), len(value))
		b.head = append(b.head, value[:count]...)
		value = value[count:]
	}
	if len(value) > 0 {
		b.truncated = true
		const tailSize = 128
		b.tail = append(b.tail, value...)
		if len(b.tail) > tailSize {
			b.tail = b.tail[len(b.tail)-tailSize:]
		}
	}
	return written, nil
}

func (b *execBuffer) String() string  { return string(append(append([]byte{}, b.head...), b.tail...)) }
func (b *execBuffer) Truncated() bool { return b.truncated }

func parseExitMarker(stdout string) (string, int, bool) {
	index := strings.LastIndex(stdout, execExitMarker+":")
	if index < 0 {
		return stdout, 0, false
	}
	lineEnd := strings.IndexByte(stdout[index:], '\n')
	if lineEnd < 0 {
		lineEnd = len(stdout) - index
	}
	value := strings.TrimSpace(stdout[index+len(execExitMarker)+1 : index+lineEnd])
	code, err := parseExitCode(value)
	if err != nil {
		return stdout, 0, false
	}
	return strings.TrimRight(stdout[:index], "\n"), code, true
}

func parseExitCode(value string) (int, error) {
	var code int
	if _, err := fmt.Sscanf(value, "%d", &code); err != nil {
		return 0, err
	}
	return code, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ golemtools.Sandbox = (*Sandbox)(nil)
