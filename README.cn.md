[English](README.md) | 中文

# LOB Codex

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](./LICENSE)

**状态：研究** — 沿 OpenAI Codex 调用链写的 Go Harness。映射是 **部分对齐**，见 [docs/CODEX_PARITY.md](./docs/CODEX_PARITY.md)。不是 Codex 替代品。

入口：`cmd/lob-codex/main.go`。

## What

Go 实现的 Session / Turn / Step：Responses API 客户端、工具路由（`echo`、`exec_command`、`apply_patch`、`write_stdin`）、系统沙箱、MCP、插件，以及 Web GUI / exec-server。

## Run in 3 commands

需要 Go 1.24+。

```bash
go test ./...
go run ./cmd/lob-codex "hello, LOB Codex"
go run ./cmd/lob-codex
```

- 带 prompt 参数：走 **`FakeModel` 的单次 turn**（`runPrompt` → `model.NewFakeClient()`），不联网。
- 无参数（或 `serve`）：起 GUI。这条路径 **必须** 有 `LOB_CODEX_API_KEY` 和 `LOB_CODEX_MODEL`（或 `OPENAI_API_KEY` / `OPENAI_MODEL`）。默认监听 `127.0.0.1:53878`。
- 可选：`go run ./cmd/lob-codex exec-server`，默认 `127.0.0.1:53879`，给远程命令执行。

也可：`go run ./cmd/lob-codex serve -addr 127.0.0.1:9000`。通过 `internal/config` 读 `.env`。额外环境变量：`LOB_CODEX_BASE_URL`、`LOB_CODEX_CONTEXT_WINDOW`、`LOB_CODEX_MAX_RETRIES`。

## Architecture

依据 `internal/session/session.go` 和 `docs/CODEX_PARITY.md`：

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

### 工具

`internal/tools/` 里已注册的 executor：

| 工具 | 文件 |
|---|---|
| `echo` | `echo.go` |
| `exec_command` | `exec_command.go` |
| `apply_patch` | `apply_patch.go` |
| `write_stdin` | `write_stdin.go` |
| MCP 工具 | `internal/mcp/client.go` 的 `Executor` |

审批：`approved`、`approved_for_session`、`approved_with_amendment`、`denied`（`router.go`）。会话级允许规则写在 gitignore 的 `tmp/exec-policy.rules`。

### 沙箱（代码里真实存在的后端）

`internal/tools/sandbox.go` 的 `localSandboxBackend()`：

- macOS：Seatbelt
- Linux：bubblewrap
- Windows：RestrictedToken（Windows 文件里还有 ConPTY）

远程 exec-server 发送明文 argv 和可移植的沙箱意图，由对端套本机后端（`internal/execserver`）。

### MCP 与插件

`internal/mcp`：stdio / HTTP 客户端，list/call、elicitation。  
`internal/extensions`：文件型目录、`plugins/*/skills/`、marketplace 安装卸载。内置 Skill 需在消息里显式触发：

- `$code-review:review`
- `$project-map:map`
- `$docs-sync:sync`

### GUI 与 CLI 的历史

App Server（`internal/appserver`）把 thread 放在 `tmp/threads/`，重启后能重新绑工作区。GUI 的会话历史恢复仍不完整（parity：rollout 有了，历史页恢复未完全对齐）。

## 许可证

Apache License 2.0，见 [LICENSE](LICENSE)。

## 联系

[chishishuan@gmail.com](mailto:chishishuan@gmail.com) · [GitHub Issues](https://github.com/lobster-bujiaban/lob-codex/issues)
