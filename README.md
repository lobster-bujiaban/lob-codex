# LOB Codex

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](./LICENSE)

从零实现一个可读、可运行、可扩展的 Coding Agent Harness。

本项目使用 Go 对 OpenAI Codex Harness 做等价实现。目标不是重新设计一个 Agent，而是沿着
Codex 的真实源码调用链逐层复现核心逻辑，并在每个阶段留下可运行、可对照的结果。

## 核心目标

- 理解模型、会话、工具和工作区如何组成 Agent Loop。
- 实现流式输出、多轮工具调用、审批与受控执行。
- 实现可恢复会话、上下文管理、MCP、Skills 与插件。
- 提供 CLI，后续再增加 Web GUI。
- 保持核心层与 OpenAI、具体模型供应商和 UI 解耦。

## 实施原则

1. 每个模块先阅读 Codex 对应源码入口和完整调用链，再写 Go 等价实现。
2. 核心状态机、事件顺序、错误语义和生命周期必须与 Codex 一致。
3. Go 只替换 Rust 语言机制，不重新设计 Codex 的主逻辑。
4. 每一阶段必须可以独立运行、演示并与 Codex 源码逐项对照。
5. 所有差异都记录在 [Codex 一致性清单](./docs/CODEX_PARITY.md)，不得隐式偏离。
6. API Key 只从环境变量或本地忽略文件读取，禁止提交。

详细步骤见 [实施计划](./docs/IMPLEMENTATION_PLAN.md)。

## 许可证

本项目使用 Apache License 2.0 开源。

## 技术路线

- Go 1.24+（本机当前为 Go 1.24.4）
- Goroutine、Channel 与 `context.Context` 管理并发和取消
- 标准库 JSON + 强类型事件协议
- Cobra 风格 CLI（首版优先标准库，确有需要再引入依赖）
- 后续可使用 Bubble Tea 增加 TUI
- OpenAI Responses API 模型适配层
- JSONL 事件存储起步，后续可替换 SQLite
- Go Module，按 `internal` package 隔离核心运行时与界面

## 当前状态

- [x] 阶段 0：建立项目定位和实施计划
- [ ] 阶段 1：最小模型对话闭环
- [ ] 阶段 2：Agent Tool Loop
- [ ] 阶段 3：会话事件与恢复
- [ ] 阶段 4：工作区、Shell、审批与沙箱
- [ ] 阶段 5：MCP、Skills 与插件
- [ ] 阶段 6：多 Agent、后台任务与上下文压缩
- [ ] 阶段 7：Web GUI 与生产化

## 运行阶段 0

```bash
go run ./cmd/lob-codex "你好，LOB Codex"
```

当前使用无需网络的 `FakeModel` 验证 CLI、Agent Runner、模型接口和事件流。运行检查：

```bash
go test ./...
go vet ./...
```

## 最小 GUI + 真实 LLM

设置 Responses API 模型配置后启动 Web 服务：

```bash
export LOB_CODEX_API_KEY="你的 API Key"
export LOB_CODEX_MODEL="你的模型名称"
# 可选，默认为 https://api.openai.com/v1
export LOB_CODEX_BASE_URL="https://api.openai.com/v1"

go run ./cmd/lob-codex
```

浏览器打开 <http://127.0.0.1:53878>。也可以使用 `-addr 127.0.0.1:9000`
临时覆盖默认地址。API Key 只保留在服务端环境变量中，不会发送到浏览器。

也兼容 `OPENAI_API_KEY`、`OPENAI_MODEL` 和 `OPENAI_BASE_URL` 环境变量。
程序启动时会自动读取当前目录的 `.env`，已有系统环境变量优先级更高。

当前最小 GUI 包含：多轮消息输入、Conversation History、流式回复、`echo` Tool Loop、
只读 `exec_command`、工具生命周期展示和错误展示。`exec_command` 当前仅支持 macOS Seatbelt，
写命令、交互进程和完整审批将在后续阶段加入。

## 交流与联系

对实现细节有疑问、发现问题或想交流 Agent Harness，可以扫码私信：

<p align="center">
  <img src="./docs/images/wechat-private-message-qr.png" alt="虾哥不加班微信私信二维码" width="220">
</p>

也欢迎通过 [GitHub Issues](https://github.com/lobster-bujiaban/lob-codex/issues) 提交可复现的问题和建议。
