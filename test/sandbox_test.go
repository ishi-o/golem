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
		if err != nil {
			t.Fatal(err)
		}
		if info.Name == name {
			return candidate
		}
	}
	t.Fatalf("tool %q was not registered", name)
	return nil
}

func invokeTool(t *testing.T, candidate tool.InvokableTool, ctx context.Context, args string) string {
	t.Helper()
	result, err := candidate.InvokableRun(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSandboxToolsShareTheBackendIndependentShellProtocol(t *testing.T) {
	backend := &fakeSandbox{removeOK: true}
	registered, err := tools.NewSandboxTools(backend, tools.SandboxToolsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := tools.UserID.With(context.Background(), "user-1")

	bash := findTool(t, registered, tools.ToolNameBash)
	output := invokeTool(t, bash, ctx, `{"command":"printf 'hello'"}`)
	if !strings.Contains(output, "bash_id: shell_") || !strings.Contains(output, "hello") {
		t.Fatalf("foreground result = %q", output)
	}

	bashOutput := findTool(t, registered, tools.ToolNameBashOutput)
	output = invokeTool(t, bashOutput, ctx, `{"bash_id":"shell_test","filter":"two"}`)
	if !strings.Contains(output, "Status: Running") || !strings.Contains(output, "line two") || strings.Contains(output, "line one") {
		t.Fatalf("filtered background result = %q", output)
	}

	kill := findTool(t, registered, tools.ToolNameKillShell)
	if output = invokeTool(t, kill, ctx, `{"bash_id":"shell_test"}`); !strings.Contains(output, "Successfully killed") {
		t.Fatalf("kill result = %q", output)
	}

	restart := findTool(t, registered, tools.ToolNameRestartSandbox)
	if output = invokeTool(t, restart, ctx, `{}`); !strings.Contains(output, "fresh sandbox") {
		t.Fatalf("restart result = %q", output)
	}
	if len(backend.ensures) != 3 || len(backend.removes) != 1 {
		t.Fatalf("backend lifecycle calls: ensures=%v removes=%v", backend.ensures, backend.removes)
	}
}

func TestCredentialToolsNeverReturnValues(t *testing.T) {
	backend := inmemoryBackend()
	registered := tools.NewCredentialTools(backend, tools.ToolNameRestartSandbox)
	ctx := tools.UserID.With(context.Background(), "user-credential")

	set := findTool(t, registered, tools.ToolNameSetCredential)
	result := invokeTool(t, set, ctx, `{"name":"API_TOKEN","value":"secret-value"}`)
	if strings.Contains(result, "secret-value") {
		t.Fatalf("set result disclosed secret: %q", result)
	}
	list := findTool(t, registered, tools.ToolNameListCredentials)
	result = invokeTool(t, list, ctx, `{}`)
	if !strings.Contains(result, "API_TOKEN") || strings.Contains(result, "secret-value") {
		t.Fatalf("list result = %q", result)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sandbox.Ensure(context.Background(), userID); err != nil {
		t.Fatalf("ensure existing pod: %v", err)
	}
	removed, err := sandbox.Remove(context.Background(), userID)
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	if _, err := client.BatchV1().Jobs("default").Get(context.Background(), "shell-job", metav1.GetOptions{}); err == nil {
		t.Fatal("shell job still exists after remove")
	}
}

func shortHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])[:16]
}
