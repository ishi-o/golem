package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/ishi-o/golem/core/store"
)

// Sandbox owns one disposable command environment per user. The Docker and
// Kubernetes modules implement this contract; core only owns the tool
// protocol shared by both backends.
type Sandbox interface {
	Ensure(ctx context.Context, userID string) (SandboxSession, error)
	Remove(ctx context.Context, userID string) (bool, error)
	io.Closer
}

// SandboxSession executes bash scripts inside one user's sandbox. stdin is
// used by background commands to upload their command file without putting
// command text in an argument list or a shell-quoted wrapper.
type SandboxSession interface {
	Exec(ctx context.Context, script string, stdin []byte, maxOutputBytes int) (SandboxExecResult, error)
}

// SandboxExecResult is the backend-neutral result of one shell exec.
type SandboxExecResult struct {
	Stdout      string
	Stderr      string
	ExitCode    int
	ExitCodeSet bool
	Truncated   bool
}

// SandboxToolsConfig controls the model-facing shell tools. Lifecycle and
// resource settings belong to each backend because a container and a Pod
// express them differently.
type SandboxToolsConfig struct {
	MaxOutputBytes  int
	DefaultTimeout  time.Duration
	MaxTimeout      time.Duration
	RestartToolName string
	Credentials     store.ShellCredentialStore
}

func (c *SandboxToolsConfig) normalize() error {
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = 30_000
	}
	if c.DefaultTimeout <= 0 {
		c.DefaultTimeout = 2 * time.Minute
	}
	if c.MaxTimeout <= 0 {
		c.MaxTimeout = 10 * time.Minute
	}
	if c.MaxTimeout < c.DefaultTimeout {
		return fmt.Errorf("sandbox tools: max timeout must not be less than default timeout")
	}
	if c.RestartToolName == "" {
		c.RestartToolName = ToolNameRestartSandbox
	}
	return nil
}

// SandboxTools are the common Bash, BashOutput, KillShell and restart
// tools. Credential tools are included when a repository is supplied.
type SandboxTools struct {
	state *sandboxTools
}

// NewSandboxTools creates the shell tools for one sandbox backend.
func NewSandboxTools(sandbox Sandbox, config SandboxToolsConfig) (*SandboxTools, error) {
	if sandbox == nil {
		return nil, errors.New("sandbox tools: nil sandbox")
	}
	if err := config.normalize(); err != nil {
		return nil, err
	}
	return &SandboxTools{state: &sandboxTools{backend: sandbox, config: config}}, nil
}

// List lists the shell tools, satisfying Builtin.
func (s *SandboxTools) List() []tool.InvokableTool {
	result := []tool.InvokableTool{s.state.bash(), s.state.bashOutput(), s.state.killShell(), s.state.restart()}
	if s.state.config.Credentials != nil {
		result = append(result, NewCredentialTools(s.state.config.Credentials, s.state.config.RestartToolName).List()...)
	}
	return result
}

// CredentialsFromRepository adapts the persistence contract for a sandbox
// backend. Backends call the resolver only while creating a fresh sandbox, so
// changing a credential takes effect after the corresponding restart tool.
func CredentialsFromRepository(repo store.ShellCredentialStore) CredentialResolver {
	return func(ctx context.Context, userID string) (map[string]string, error) {
		if repo == nil {
			return nil, nil
		}
		credentials, err := repo.ListByOwner(ctx, userID)
		if err != nil {
			return nil, err
		}
		result := make(map[string]string, len(credentials))
		for _, credential := range credentials {
			if IsValidCredentialName(credential.Name) {
				result[credential.Name] = credential.Value
			}
		}
		return result, nil
	}
}

// CredentialResolver supplies plaintext values only while a backend is
// creating a sandbox. A Docker backend injects them into the new container;
// a Kubernetes backend writes them into a Secret. Callers may provide a
// resolver backed by an encrypted store rather than using the helper above.
type CredentialResolver func(context.Context, string) (map[string]string, error)

var credentialNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// IsValidCredentialName reports whether name is safe as a POSIX environment
// variable and a credential filename.
func IsValidCredentialName(name string) bool {
	return credentialNamePattern.MatchString(name)
}

type sandboxTools struct {
	backend Sandbox
	config  SandboxToolsConfig
}

type bashInput struct {
	Command         string `json:"command"`
	TimeoutMS       *int64 `json:"timeout,omitempty"`
	Description     string `json:"description,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

func (s *sandboxTools) bash() tool.InvokableTool {
	return MustTool(utils.InferTool(
		ToolNameBash,
		`Execute a bash command for terminal operations such as npm, docker, make, mvn or python.

Usage notes:
- The command argument is required.
- Optional timeout is in milliseconds; the default is 120000 and the maximum is 600000.
- Use run_in_background for long-running commands, then use BashOutput to read new output.
- Quote file paths with spaces in double quotes.
- Chain dependent commands with &&. Use ; if earlier failures are acceptable.
- Prefer absolute paths over cd.`,
		func(ctx context.Context, in bashInput) (string, error) {
			userID, err := UserID.Require(ctx)
			if err != nil {
				return "Error: " + err.Error(), nil
			}
			if strings.TrimSpace(in.Command) == "" {
				return "Error: command is required", nil
			}
			bashID := newShellID()
			session, err := s.backend.Ensure(ctx, userID)
			if err != nil {
				return fmt.Sprintf("bash_id: %s\n\nError creating sandbox: %v", bashID, err), nil
			}
			if in.RunInBackground {
				return s.runBackground(ctx, session, bashID, in.Command), nil
			}

			timeout := s.timeout(in.TimeoutMS)
			execCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			result, err := session.Exec(execCtx, "touch /tmp/.last_activity 2>/dev/null || true\n"+in.Command, nil, s.config.MaxOutputBytes)
			if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
				return fmt.Sprintf("bash_id: %s\n\nCommand timed out after %dms", bashID, timeout.Milliseconds()), nil
			}
			if err != nil {
				return fmt.Sprintf("bash_id: %s\n\nError executing command: %v", bashID, err), nil
			}
			return formatForeground(bashID, result, s.config.MaxOutputBytes), nil
		},
	))
}

type bashOutputInput struct {
	BashID string `json:"bash_id"`
	Filter string `json:"filter,omitempty"`
}

func (s *sandboxTools) bashOutput() tool.InvokableTool {
	return MustTool(utils.InferTool(
		ToolNameBashOutput,
		`Retrieve output from a running or completed background bash shell.
- Always returns only new output since the last check.
- Returns stdout and stderr output along with shell status.
- An optional regular expression filters output lines.
- Use this tool to monitor a long-running shell.`,
		func(ctx context.Context, in bashOutputInput) (string, error) {
			if !isSafeShellID(in.BashID) {
				return "Error: invalid bash_id", nil
			}
			userID, err := UserID.Require(ctx)
			if err != nil {
				return "Error: " + err.Error(), nil
			}
			session, err := s.backend.Ensure(ctx, userID)
			if err != nil {
				return "Error retrieving output: " + err.Error(), nil
			}
			script := backgroundOutputScript(in.BashID, s.config.MaxOutputBytes)
			execCtx, cancel := context.WithTimeout(ctx, s.config.DefaultTimeout)
			defer cancel()
			result, err := session.Exec(execCtx, script, nil, s.config.MaxOutputBytes+512)
			if err != nil {
				return "Error retrieving output: " + err.Error(), nil
			}
			output, status := splitBackgroundOutput(result.Stdout)
			if strings.TrimSpace(output) == "NOT_FOUND" {
				return "Error: No background shell found with ID: " + in.BashID, nil
			}
			if in.Filter != "" {
				output = filterOutput(output, in.Filter)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Shell ID: %s\nStatus: %s\n", in.BashID, status)
			if output != "" {
				b.WriteString("\nNew output:\n")
				b.WriteString(output)
			} else {
				b.WriteString("\nNo new output since last check.")
			}
			return b.String(), nil
		},
	))
}

func (s *sandboxTools) killShell() tool.InvokableTool {
	return MustTool(utils.InferTool(
		ToolNameKillShell,
		`Kill a running background bash shell by its bash_id and return its status.`,
		func(ctx context.Context, in struct {
			BashID string `json:"bash_id"`
		}) (string, error) {
			if !isSafeShellID(in.BashID) {
				return "Error: invalid bash_id", nil
			}
			userID, err := UserID.Require(ctx)
			if err != nil {
				return "Error: " + err.Error(), nil
			}
			session, err := s.backend.Ensure(ctx, userID)
			if err != nil {
				return "Error killing shell: " + err.Error(), nil
			}
			script := killBackgroundScript(in.BashID)
			execCtx, cancel := context.WithTimeout(ctx, s.config.DefaultTimeout)
			defer cancel()
			result, err := session.Exec(execCtx, script, nil, 1024)
			if err != nil {
				return "Error killing shell: " + err.Error(), nil
			}
			switch strings.TrimSpace(result.Stdout) {
			case "NOT_FOUND":
				return "Error: No background shell found with ID: " + in.BashID, nil
			case "already_terminated":
				return "Shell " + in.BashID + " was already terminated. Removed from active storage.", nil
			default:
				return "Successfully killed shell: " + in.BashID, nil
			}
		},
	))
}

func (s *sandboxTools) restart() tool.InvokableTool {
	name := s.config.RestartToolName
	description := fmt.Sprintf(`Restart the user's shell sandbox.
- Use after changing a credential so the next Bash call sees the new value.
- Background shells and files outside the persistent working directory are lost.
- The next Bash call creates a fresh sandbox when no sandbox is running.`)
	return MustTool(utils.InferTool(
		name,
		description,
		func(ctx context.Context, _ struct{}) (string, error) {
			userID, err := UserID.Require(ctx)
			if err != nil {
				return "Error: " + err.Error(), nil
			}
			removed, err := s.backend.Remove(ctx, userID)
			if err != nil {
				return "Error restarting shell sandbox: " + err.Error(), nil
			}
			if !removed {
				return "No running shell sandbox was found. The next Bash call will create one.", nil
			}
			return "Shell sandbox restarted. The next Bash call will create a fresh sandbox with updated credentials.", nil
		},
	))
}

func (s *sandboxTools) runBackground(ctx context.Context, session SandboxSession, bashID, command string) string {
	script := backgroundStartScript(bashID)
	execCtx, cancel := context.WithTimeout(ctx, s.config.DefaultTimeout)
	defer cancel()
	if _, err := session.Exec(execCtx, script, []byte(command), s.config.MaxOutputBytes); err != nil {
		return fmt.Sprintf("bash_id: %s\n\nError starting background shell: %v", bashID, err)
	}
	return fmt.Sprintf("bash_id: %s\n\nBackground shell started with ID: %s\nUse BashOutput with bash_id='%s' to retrieve output.", bashID, bashID, bashID)
}

func (s *sandboxTools) timeout(requested *int64) time.Duration {
	milliseconds := s.config.DefaultTimeout.Milliseconds()
	if requested != nil && *requested != 0 {
		milliseconds = *requested
	}
	maximum := s.config.MaxTimeout.Milliseconds()
	if milliseconds < 1 {
		milliseconds = 1
	}
	if milliseconds > maximum {
		milliseconds = maximum
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func formatForeground(bashID string, result SandboxExecResult, maxOutputBytes int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "bash_id: %s\n\n", bashID)
	if result.Stdout != "" {
		b.WriteString(result.Stdout)
	}
	if result.Stderr != "" {
		if result.Stdout != "" {
			b.WriteByte('\n')
		}
		b.WriteString("STDERR:\n")
		b.WriteString(result.Stderr)
	}
	if result.ExitCodeSet && result.ExitCode != 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "Exit code: %d", result.ExitCode)
	}
	if result.Truncated {
		fmt.Fprintf(&b, "\n... (output truncated at %d bytes)", maxOutputBytes)
	}
	return truncateShellOutput(b.String(), maxOutputBytes+256)
}

const backgroundStatusMarker = "--GOLEM-SANDBOX-STATUS--"

func backgroundStartScript(bashID string) string {
	return strings.Join([]string{
		"set -e",
		"mkdir -p /tmp/.bg",
		"touch /tmp/.last_activity 2>/dev/null || true",
		"cat > /tmp/.bg/" + bashID + ".cmd",
		"setsid bash /tmp/.bg/" + bashID + ".cmd > /tmp/.bg/" + bashID + ".out 2>&1 < /dev/null &",
		"PID=$!",
		"echo $PID > /tmp/.bg/" + bashID + ".pid",
		"echo 0 > /tmp/.bg/" + bashID + ".offset",
	}, "\n")
}

func backgroundOutputScript(bashID string, maxOutputBytes int) string {
	max := strconv.Itoa(maxOutputBytes)
	return strings.Join([]string{
		"touch /tmp/.last_activity 2>/dev/null || true",
		"BG=/tmp/.bg/" + bashID,
		"if [ ! -f \"$BG.pid\" ]; then echo NOT_FOUND; exit 0; fi",
		"OFF=$(cat \"$BG.offset\" 2>/dev/null || echo 0)",
		"SIZE=$(stat -c %s \"$BG.out\" 2>/dev/null || echo 0)",
		"MAX=" + max,
		"AVAIL=$((SIZE-OFF))",
		"if [ \"$AVAIL\" -gt \"$MAX\" ]; then NEW=$MAX; TRUNC=1; else NEW=$AVAIL; TRUNC=0; fi",
		"if [ \"$NEW\" -gt 0 ]; then",
		"  dd if=\"$BG.out\" bs=1 skip=$OFF count=$NEW 2>/dev/null",
		"  echo $((OFF+NEW)) > \"$BG.offset\"",
		"fi",
		"if [ \"$TRUNC\" = 1 ]; then echo; echo \"... (output truncated at $MAX bytes; call BashOutput again for more)\"; fi",
		"echo " + backgroundStatusMarker,
		"if kill -0 $(cat \"$BG.pid\") 2>/dev/null; then echo Running; else echo Completed; fi",
	}, "\n")
}

func killBackgroundScript(bashID string) string {
	return strings.Join([]string{
		"touch /tmp/.last_activity 2>/dev/null || true",
		"BG=/tmp/.bg/" + bashID,
		"if [ ! -f \"$BG.pid\" ]; then echo NOT_FOUND; exit 0; fi",
		"PID=$(cat \"$BG.pid\")",
		"if kill -0 $PID 2>/dev/null; then",
		"  kill -TERM -$PID 2>/dev/null || kill -TERM $PID 2>/dev/null || true",
		"  sleep 1",
		"  kill -KILL -$PID 2>/dev/null || kill -KILL $PID 2>/dev/null || true",
		"  STATUS=killed",
		"else",
		"  STATUS=already_terminated",
		"fi",
		"rm -f \"$BG.pid\" \"$BG.out\" \"$BG.offset\" \"$BG.cmd\"",
		"echo $STATUS",
	}, "\n")
}

func splitBackgroundOutput(stdout string) (string, string) {
	idx := strings.LastIndex(stdout, backgroundStatusMarker)
	if idx < 0 {
		return strings.TrimRight(stdout, "\n"), "Unknown"
	}
	output := strings.TrimRight(stdout[:idx], "\n")
	status := strings.TrimSpace(stdout[idx+len(backgroundStatusMarker):])
	if status == "" {
		status = "Unknown"
	}
	return output, status
}

func filterOutput(output, expression string) string {
	pattern, err := regexp.Compile(expression)
	if err != nil {
		return output
	}
	var b strings.Builder
	for _, line := range strings.Split(output, "\n") {
		if pattern.FindStringIndex(line) != nil {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func truncateShellOutput(output string, max int) string {
	if max <= 0 || len(output) <= max {
		return output
	}
	headerEnd := strings.Index(output, "\n\n")
	if headerEnd <= 0 || headerEnd+2 >= max {
		return output[:max] + "\n... (output truncated)"
	}
	header := output[:headerEnd+2]
	room := max - len(header)
	return header + output[headerEnd+2:headerEnd+2+minInt(room, len(output)-headerEnd-2)] + "\n... (output truncated)"
}

func isSafeShellID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func newShellID() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		return "shell_" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("shell_%x", shellSequence.Add(1))
}

var shellSequence atomic.Uint64

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// CredentialTools are the three tools that manage credentials without ever
// returning a stored value to the model.
type CredentialTools struct {
	repo            store.ShellCredentialStore
	restartToolName string
}

// NewCredentialTools creates the credential tools over one repository. A
// nil repository yields a family whose List is empty.
func NewCredentialTools(repo store.ShellCredentialStore, restartToolName string) *CredentialTools {
	if repo == nil {
		return &CredentialTools{}
	}
	if restartToolName == "" {
		restartToolName = ToolNameRestartSandbox
	}
	return &CredentialTools{repo: repo, restartToolName: restartToolName}
}

// List lists the credential tools, satisfying Builtin.
func (c *CredentialTools) List() []tool.InvokableTool {
	if c.repo == nil {
		return nil
	}
	repo, restartToolName := c.repo, c.restartToolName
	return []tool.InvokableTool{
		MustTool(utils.InferTool(
			ToolNameSetCredential,
			"Store a credential for the user's shell sandbox. The name becomes an environment variable and the value is never returned.",
			func(ctx context.Context, in struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			}) (string, error) {
				userID, err := UserID.Require(ctx)
				if err != nil {
					return "Error: " + err.Error(), nil
				}
				if !IsValidCredentialName(in.Name) {
					return "Error: invalid credential name. Use a POSIX environment-variable name.", nil
				}
				if len([]byte(in.Value)) > 64*1024 {
					return "Error: credential value is larger than 65536 bytes", nil
				}
				if err := repo.Save(ctx, store.ShellCredential{ID: store.ShellCredentialID(userID, in.Name), OwnerID: userID, Name: in.Name, Value: in.Value}); err != nil {
					return "Error storing credential: " + err.Error(), nil
				}
				return "Credential " + in.Name + " stored. Run " + restartToolName + " to expose it in the next sandbox.", nil
			},
		)),
		MustTool(utils.InferTool(
			ToolNameListCredentials,
			"List the names of the user's shell credentials. Values are never returned.",
			func(ctx context.Context, _ struct{}) (string, error) {
				userID, err := UserID.Require(ctx)
				if err != nil {
					return "Error: " + err.Error(), nil
				}
				credentials, err := repo.ListByOwner(ctx, userID)
				if err != nil {
					return "Error listing credentials: " + err.Error(), nil
				}
				if len(credentials) == 0 {
					return "No credentials stored.", nil
				}
				sort.Slice(credentials, func(i, j int) bool { return credentials[i].Name < credentials[j].Name })
				var b strings.Builder
				b.WriteString("Credentials:\n")
				for _, credential := range credentials {
					b.WriteString("- ")
					b.WriteString(credential.Name)
					b.WriteByte('\n')
				}
				return strings.TrimRight(b.String(), "\n"), nil
			},
		)),
		MustTool(utils.InferTool(
			ToolNameDeleteCredential,
			"Remove a credential from the user's shell sandbox. Restart the sandbox afterwards so it disappears from the environment.",
			func(ctx context.Context, in struct {
				Name string `json:"name"`
			}) (string, error) {
				userID, err := UserID.Require(ctx)
				if err != nil {
					return "Error: " + err.Error(), nil
				}
				if !IsValidCredentialName(in.Name) {
					return "Error: invalid credential name. Use a POSIX environment-variable name.", nil
				}
				if err := repo.DeleteByOwnerAndName(ctx, userID, in.Name); err != nil {
					return "Error deleting credential: " + err.Error(), nil
				}
				return "Credential " + in.Name + " removed. Run " + restartToolName + " to drop it from the sandbox.", nil
			},
		)),
	}
}
