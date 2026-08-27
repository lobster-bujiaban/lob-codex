# LOB Codex

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](./LICENSE)

从零实现一个可读、可运行、可扩展的 Coding Agent Harness。

本项目以 OpenAI Codex 源码作为架构参考，以 LOB Harness 作为功能与教学工程参考，
但不直接复制实现。目标是按最小闭环逐层构建，并在每个阶段留下可运行结果。

## 核心目标

- 理解模型、会话、工具和工作区如何组成 Agent Loop。
- 实现流式输出、多轮工具调用、审批与受控执行。
- 实现可恢复会话、上下文管理、MCP、Skills 与插件。
- 提供 CLI，后续再增加 Web GUI。
- 保持核心层与 OpenAI、具体模型供应商和 UI 解耦。

## 实施原则

1. 每一阶段必须可以独立运行和演示。
2. 先实现正确的数据流，再增加 UI 和高级能力。
3. 事件是事实来源，界面和会话状态由事件投影得到。
4. 所有工具统一经过注册、校验、审批、执行和结果回传。
5. API Key 只从环境变量或本地忽略文件读取，禁止提交。

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
