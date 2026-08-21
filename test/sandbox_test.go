package agent_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/ishi-o/golem/core/dao"
	"github.com/ishi-o/golem/core/dao/inmemory"
	"github.com/ishi-o/golem/core/tools"
	kubesandbox "github.com/ishi-o/golem/sandbox/kubernetes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

type fakeSandbox struct {
	mu       sync.Mutex
	session  fakeSession
	ensures  []string
	removes  []string
	removeOK bool
}

func (f *fakeSandbox) Ensure(_ context.Context, userID string) (tools.SandboxSession, error) {
	f.mu.Lock()
	f.ensures = append(f.ensures, userID)
	f.mu.Unlock()
	return &f.session, nil
}

func (f *fakeSandbox) Remove(_ context.Context, userID string) (bool, error) {
	f.mu.Lock()
	f.removes = append(f.removes, userID)
	f.mu.Unlock()
	return f.removeOK, nil
}

func (f *fakeSandbox) Close() error { return nil }

type fakeSession struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeSession) Exec(_ context.Context, script string, _ []byte, _ int) (tools.SandboxExecResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, script)
	f.mu.Unlock()
	switch {
	case strings.Contains(script, "--GOLEM-SANDBOX-STATUS--"):
		return tools.SandboxExecResult{Stdout: "line one\nline two\n--GOLEM-SANDBOX-STATUS--\nRunning\n"}, nil
	case strings.Contains(script, "PID=$(cat"):
		return tools.SandboxExecResult{Stdout: "killed\n"}, nil
	case strings.Contains(script, "setsid bash"):
		return tools.SandboxExecResult{}, nil
	default:
		return tools.SandboxExecResult{Stdout: "hello\n", ExitCodeSet: true}, nil
	}
}

func findTool(t *testing.T, all []tool.InvokableTool, name string) tool.InvokableTool {
	t.Helper()
	for _, candidate := range all {
		info, err := candidate.Info(context.Background())
		require.NoError(t, err)
		if info.Name == name {
			return candidate
		}
	}
	require.FailNow(t, "tool was not registered: "+name)
	return nil
}

func invokeTool(t *testing.T, candidate tool.InvokableTool, ctx context.Context, args string) string {
	t.Helper()
	result, err := candidate.InvokableRun(ctx, args)
	require.NoError(t, err)
	return result
}

func TestSandboxToolsShareTheBackendIndependentShellProtocol(t *testing.T) {
	backend := &fakeSandbox{removeOK: true}
	registered, err := tools.NewSandboxTools(backend, tools.SandboxToolsConfig{})
	require.NoError(t, err)
	ctx := tools.UserID.With(context.Background(), "user-1")

	bash := findTool(t, registered, tools.ToolNameBash)
	output := invokeTool(t, bash, ctx, `{"command":"printf 'hello'"}`)
	assert.Contains(t, output, "bash_id: shell_")
	assert.Contains(t, output, "hello")

	bashOutput := findTool(t, registered, tools.ToolNameBashOutput)
	output = invokeTool(t, bashOutput, ctx, `{"bash_id":"shell_test","filter":"two"}`)
	assert.Contains(t, output, "Status: Running")
	assert.Contains(t, output, "line two")
	assert.NotContains(t, output, "line one")

	kill := findTool(t, registered, tools.ToolNameKillShell)
	output = invokeTool(t, kill, ctx, `{"bash_id":"shell_test"}`)
	assert.Contains(t, output, "Successfully killed")

	restart := findTool(t, registered, tools.ToolNameRestartSandbox)
	output = invokeTool(t, restart, ctx, `{}`)
	assert.Contains(t, output, "fresh sandbox")
	assert.Len(t, backend.ensures, 3)
	assert.Len(t, backend.removes, 1)
}

func TestCredentialToolsNeverReturnValues(t *testing.T) {
	backend := inmemoryBackend()
	registered := tools.NewCredentialTools(backend, tools.ToolNameRestartSandbox)
	ctx := tools.UserID.With(context.Background(), "user-credential")

	set := findTool(t, registered, tools.ToolNameSetCredential)
	result := invokeTool(t, set, ctx, `{"name":"API_TOKEN","value":"secret-value"}`)
	assert.NotContains(t, result, "secret-value")
	list := findTool(t, registered, tools.ToolNameListCredentials)
	result = invokeTool(t, list, ctx, `{}`)
	assert.Contains(t, result, "API_TOKEN")
	assert.NotContains(t, result, "secret-value")
}

func inmemoryBackend() dao.ShellCredentialRepo {
	return inmemory.New().ShellCredentials()
}

func TestKubernetesSandboxReusesAndRemovesLabeledUserJobs(t *testing.T) {
	userID := "kube-user"
	owner := shortHash(userID)
	client := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "shell-pod", Namespace: "default", Labels: map[string]string{kubesandbox.LabelSandbox: "true", kubesandbox.LabelOwner: owner, kubesandbox.LabelRole: kubesandbox.LabelRoleUser}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&v1.Job{ObjectMeta: metav1.ObjectMeta{Name: "shell-job", Namespace: "default", Labels: map[string]string{kubesandbox.LabelSandbox: "true", kubesandbox.LabelOwner: owner, kubesandbox.LabelRole: kubesandbox.LabelRoleUser}}},
	)
	sandbox, err := kubesandbox.New(kubesandbox.Config{
		Client:     client,
		RESTConfig: &rest.Config{Host: "https://kube.test"},
		Image:      "golem-shell:latest",
		WorkingDir: "/workspace",
		PVCMounts:  []kubesandbox.PVCMount{{PVCName: "workspace", MountPath: "/workspace"}},
	})
	require.NoError(t, err)
	_, err = sandbox.Ensure(context.Background(), userID)
	require.NoError(t, err)
	removed, err := sandbox.Remove(context.Background(), userID)
	require.NoError(t, err)
	assert.True(t, removed)
	_, err = client.BatchV1().Jobs("default").Get(context.Background(), "shell-job", metav1.GetOptions{})
	assert.Error(t, err, "shell job still exists after remove")
}

func shortHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])[:16]
}
