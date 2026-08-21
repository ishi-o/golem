package kubernetes

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	golemtools "github.com/ishi-o/golem/core/tools"
)

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
