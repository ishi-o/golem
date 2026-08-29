package docker

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	golemtools "github.com/ishi-o/golem/core/tools"
)

type session struct {
	sandbox *Sandbox
	id      string
}

func (s session) Exec(ctx context.Context, script string, stdin []byte, maxOutputBytes int) (golemtools.SandboxExecResult, error) {
	if maxOutputBytes <= 0 {
		maxOutputBytes = 30_000
	}
	created, err := s.sandbox.client.ExecCreate(ctx, s.id, client.ExecCreateOptions{Cmd: []string{"bash", "-c", script}, AttachStdin: len(stdin) > 0, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return golemtools.SandboxExecResult{}, fmt.Errorf("create container exec: %w", err)
	}
	attached, err := s.sandbox.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
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
	inspect, err := s.sandbox.client.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
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
