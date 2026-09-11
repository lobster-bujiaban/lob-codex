English | [中文](README.cn.md)

# LOB Codex

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](./LICENSE)

**Status: research** — Go harness that follows OpenAI Codex call chains. Mapping is **partial**; see [docs/CODEX_PARITY.md](./docs/CODEX_PARITY.md). Not a drop-in Codex.

Entry: `cmd/lob-codex/main.go`.

## What

Session / Turn / Step runtime in Go: Responses API client, tool router (`echo`, `exec_command`, `apply_patch`, `write_stdin`), OS sandbox, MCP, plugins, and a Web GUI / exec-server.

## Run in 3 commands

Requires Go 1.24+.

```bash
go test ./...
go run ./cmd/lob-codex "hello, LOB Codex"
go run ./cmd/lob-codex
```

- A prompt argument runs **one turn with `FakeModel`** (`runPrompt` → `model.NewFakeClient()`). No network.
- No arguments (or `serve`) starts the GUI. That path **requires** `LOB_CODEX_API_KEY` and `LOB_CODEX_MODEL` (or `OPENAI_API_KEY` / `OPENAI_MODEL`). Default listen: `127.0.0.1:53878`.
- Optional: `go run ./cmd/lob-codex exec-server` on `127.0.0.1:53879` for remote command execution.

Also: `go run ./cmd/lob-codex serve -addr 127.0.0.1:9000`. Loads `.env` via `internal/config`. Extra env: `LOB_CODEX_BASE_URL`, `LOB_CODEX_CONTEXT_WINDOW`, `LOB_CODEX_MAX_RETRIES`.

## Architecture

From `internal/session/session.go` and `docs/CODEX_PARITY.md`:

```text
CLI prompt | App Server (internal/appserver)
  → Session.IO Submit / event stream
  → submissionLoop → RegularTask
  → runTurn / runSamplingRequest (Turn / Step)
  → model.Client  (FakeClient | OpenAI Responses SSE)
  → tools.Router
       echo | exec_command | apply_patch | write_stdin | MCP tools
  → sandbox + approval
  → ConversationHistory + JSONL rollout
```

### Tools

Registered executors under `internal/tools/`:

| Tool | File |
|---|---|
| `echo` | `echo.go` |
| `exec_command` | `exec_command.go` |
| `apply_patch` | `apply_patch.go` |
| `write_stdin` | `write_stdin.go` |
| MCP tools | `internal/mcp/client.go` `Executor` |

Approvals: `approved`, `approved_for_session`, `approved_with_amendment`, `denied` (`router.go`). Session-scoped allow rules persist in gitignored `tmp/exec-policy.rules`.

### Sandbox (real backends)

`localSandboxBackend()` in `internal/tools/sandbox.go`:

- macOS: Seatbelt
- Linux: bubblewrap
- Windows: RestrictedToken (+ ConPTY in the Windows files)

Remote exec-server sends plaintext argv plus portable sandbox intent; the peer applies the local backend (`internal/execserver`).

### MCP and plugins

`internal/mcp`: stdio and HTTP clients, list/call, elicitation.  
`internal/extensions`: file-backed catalog, skills under `plugins/*/skills/`, marketplace install/uninstall. Built-in skills (explicit trigger in the message):

- `$code-review:review`
- `$project-map:map`
- `$docs-sync:sync`

### GUI vs CLI history

App Server (`internal/appserver`) keeps threads under `tmp/threads/` and can rebind workspace after restart. Conversation-history restore for the GUI is still incomplete (parity doc: rollout exists; history page restore is not fully aligned).

## License

Apache License 2.0. See [LICENSE](LICENSE).

## Contact

[chishishuan@gmail.com](mailto:chishishuan@gmail.com) · [GitHub Issues](https://github.com/lobster-bujiaban/lob-codex/issues)
