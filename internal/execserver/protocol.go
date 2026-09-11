package execserver

import "encoding/json"

const (
	MethodProcessStart = "process/start"
	MethodProcessWrite = "process/write"
	MethodProcessPoll  = "process/poll"
	MethodProcessKill  = "process/kill"

	WindowsSandboxRestrictedToken = "restricted_token"
)

// SandboxIntent is the portable sandbox policy Codex sends instead of a host wrapper.
type SandboxIntent struct {
	WorkspaceWrite        bool     `json:"workspace_write"`
	NetworkAccess         bool     `json:"network_access"`
	CWD                   string   `json:"cwd"`
	WorkspaceRoots        []string `json:"workspace_roots"`
	WindowsSandboxLevel   string   `json:"windows_sandbox_level,omitempty"`
	EnforceManagedNetwork bool     `json:"enforce_managed_network,omitempty"`
}

// ExecParams is the Codex ExecParams subset used to start a remote process.
type ExecParams struct {
	ProcessID string         `json:"process_id"`
	Command   string         `json:"command"`
	Argv      []string       `json:"argv"`
	CWD       string         `json:"cwd"`
	TTY       bool           `json:"tty"`
	Sandbox   *SandboxIntent `json:"sandbox,omitempty"`
}

type WriteParams struct {
	ProcessID string `json:"process_id"`
	Chars     string `json:"chars"`
}

type PollParams struct {
	ProcessID string `json:"process_id"`
	WaitMS    int64  `json:"wait_ms"`
}

type KillParams struct {
	ProcessID string `json:"process_id"`
}

type PollResult struct {
	Output   string `json:"output"`
	Running  bool   `json:"running"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
