package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type SandboxPolicy struct {
	WorkspaceRoot    string
	WorkingDirectory string
	WorkspaceWrite   bool
	NetworkAccess    bool
}

type SandboxBackend interface {
	Name() string
	Command(context.Context, SandboxPolicy, string) (*exec.Cmd, error)
}

func localSandboxBackend() SandboxBackend {
	switch runtime.GOOS {
	case "darwin":
		return seatbeltBackend{}
	case "linux":
		return bubblewrapBackend{}
	case "windows":
		return windowsRestrictedTokenBackend{}
	default:
		return unsupportedSandboxBackend{platform: runtime.GOOS}
	}
}

// SandboxedCommand wraps a shell command with the host OS sandbox.
func SandboxedCommand(ctx context.Context, policy SandboxPolicy, command string) (*exec.Cmd, string, error) {
	backend := localSandboxBackend()
	cmd, err := backend.Command(ctx, policy, command)
	if err != nil {
		return nil, backend.Name(), err
	}
	return cmd, backend.Name(), nil
}

// NativeShellArgv is the unwrapped command sent to a remote exec-server.
func NativeShellArgv(command string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/C", command}
	}
	return []string{"/bin/sh", "-c", command}
}

func shellCommandFromArgv(argv []string) string {
	switch {
	case len(argv) >= 3 && (argv[0] == "/bin/sh" || argv[0] == "/bin/zsh" || strings.EqualFold(filepath.Base(argv[0]), "cmd.exe")) && (argv[1] == "-c" || strings.EqualFold(argv[1], "/C")):
		return argv[2]
	case len(argv) == 0:
		return ""
	default:
		return strings.Join(argv, " ")
	}
}

type seatbeltBackend struct{}

func (seatbeltBackend) Name() string { return "macos-seatbelt" }
func (seatbeltBackend) Command(ctx context.Context, policy SandboxPolicy, command string) (*exec.Cmd, error) {
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		return nil, errors.New("Seatbelt sandbox is unavailable")
	}
	writeRule := ""
	if policy.WorkspaceWrite {
		writeRule = fmt.Sprintf("\n(allow file-write* (subpath %q))", policy.WorkspaceRoot)
		for _, protected := range []string{".git", ".codex"} {
			path := filepath.Join(policy.WorkspaceRoot, protected)
			if info, err := os.Stat(path); err == nil {
				if info.IsDir() {
					writeRule += fmt.Sprintf("\n(deny file-write* (subpath %q))", path)
				} else {
					writeRule += fmt.Sprintf("\n(deny file-write* (literal %q))", path)
				}
			}
		}
	}
	networkRule := ""
	if policy.NetworkAccess {
		networkRule = "\n(allow network*)"
	}
	profile := fmt.Sprintf(`(version 1)
(deny default)
(import "system.sb")
(allow process-exec)
(allow process-fork)
(allow signal (target self))
(allow sysctl-read)
(allow file-read* (subpath "/System") (subpath "/usr") (subpath "/bin") (subpath "/sbin") (subpath "/Library") (subpath "/opt/homebrew") (subpath "/usr/local") (subpath "/Applications/ChatGPT.app/Contents/Resources") (subpath "/Applications/Codex.app/Contents/Resources") (subpath %q))%s%s`, policy.WorkspaceRoot, writeRule, networkRule)
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", profile, "/bin/zsh", "-c", command)
	cmd.Dir = policy.WorkingDirectory
	cmd.Env = append(os.Environ(), "PATH="+execSearchPath(), fmt.Sprintf("CODEX_SANDBOX_NETWORK_DISABLED=%d", boolInt(!policy.NetworkAccess)))
	return cmd, nil
}

type bubblewrapBackend struct{}

func (bubblewrapBackend) Name() string { return "linux-bubblewrap" }
func (bubblewrapBackend) Command(ctx context.Context, policy SandboxPolicy, command string) (*exec.Cmd, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, errors.New("Linux sandbox requires bubblewrap (bwrap)")
	}
	args := []string{"--die-with-parent", "--new-session", "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--chdir", policy.WorkingDirectory}
	if policy.WorkspaceWrite {
		args = append(args, "--bind", policy.WorkspaceRoot, policy.WorkspaceRoot)
		for _, protected := range []string{".git", ".codex"} {
			path := filepath.Join(policy.WorkspaceRoot, protected)
			if _, err := os.Stat(path); err == nil {
				args = append(args, "--ro-bind", path, path)
			}
		}
	}
	if !policy.NetworkAccess {
		args = append(args, "--unshare-net")
	}
	args = append(args, "/bin/sh", "-c", command)
	cmd := exec.CommandContext(ctx, bwrap, args...)
	cmd.Dir = policy.WorkingDirectory
	cmd.Env = append(os.Environ(), "PATH="+execSearchPath(), fmt.Sprintf("CODEX_SANDBOX_NETWORK_DISABLED=%d", boolInt(!policy.NetworkAccess)))
	return cmd, nil
}

type unsupportedSandboxBackend struct{ platform string }

func (backend unsupportedSandboxBackend) Name() string { return "unsupported-" + backend.platform }
func (backend unsupportedSandboxBackend) Command(context.Context, SandboxPolicy, string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("sandbox is required but unavailable on %s", backend.platform)
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// FinishSandboxStart attaches host-specific job/token cleanup after spawn.
func FinishSandboxStart(cmd *exec.Cmd) error {
	return finishSandboxStart(cmd)
}
