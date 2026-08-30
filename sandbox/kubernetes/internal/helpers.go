package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	golemtools "github.com/ishi-o/golem/core/tools"
)

// buildJob assembles the shell Job for one identity. key is the identity's
// composite key — what the labels and GenerateName carry — while the
// per-user subpaths inside the mounts use the user's own id, so a user's
// PVC directory is shared by every scope they run in.
func (s *Sandbox) buildJob(identity golemtools.SandboxIdentity, key, secretName string) *batchv1.Job {
	userID := identity.UserID
	labelsForUser := map[string]string{LabelSandbox: "true", LabelOwner: userKey(key), LabelRole: LabelRoleUser}
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
		// The group's and tenant's directories, each at the path the storage
		// layout gives it, so the shell sees the same view the file tools
		// do. One extra mount per scope per PVC, only when the identity
		// carries the scope.
		if identity.GroupID != "" {
			name := fmt.Sprintf("group-mount-%d", i)
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      name,
				MountPath: path.Join(pvcMount.MountPath, "groups", identity.GroupID),
				SubPath:   s.scopeSubPath("groups", identity.GroupID, pvcMount),
				ReadOnly:  false,
			})
			volumes = append(volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcMount.PVCName}}})
		}
		if identity.TenantID != "" {
			name := fmt.Sprintf("tenant-mount-%d", i)
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      name,
				MountPath: path.Join(pvcMount.MountPath, "tenants", identity.TenantID),
				SubPath:   s.scopeSubPath("tenants", identity.TenantID, pvcMount),
				ReadOnly:  false,
			})
			volumes = append(volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcMount.PVCName}}})
		}
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
		ObjectMeta: metav1.ObjectMeta{GenerateName: "golem-shell-" + userKey(key) + "-", Labels: labelsForUser},
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

// scopeSubPath is the PVC subpath of a group's or tenant's directory,
// namespaced under the same segment the storage layout gives it. The scope
// id is hashed like a user id: the PVC's layout stays flat and collision-
// free whatever the channel mints.
func (s *Sandbox) scopeSubPath(scope, scopeID string, mount PVCMount) string {
	return path.Join(mount.SubPathPrefix, scope, userKey(scopeID))
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
