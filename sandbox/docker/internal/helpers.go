package docker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/client"

	golemtools "github.com/ishi-o/golem/core/tools"
)

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
	if _, err := s.client.ContainerStop(ctx, id, client.ContainerStopOptions{}); err != nil && !isNoSuchObject(err) {
		return fmt.Errorf("stop sandbox container: %w", err)
	}
	if _, err := s.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !isNoSuchObject(err) {
		return fmt.Errorf("remove sandbox container: %w", err)
	}
	return nil
}

func (s *Sandbox) isRunning(ctx context.Context, id string) (bool, error) {
	info, err := s.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return false, err
	}
	return info.Container.State != nil && info.Container.State.Running, nil
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
