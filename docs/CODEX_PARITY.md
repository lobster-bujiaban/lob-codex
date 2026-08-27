# Codex 源码一致性清单

本文件记录 LOB Codex 与 OpenAI Codex 源码的逐项映射。完成意味着 Go 实现保留了 Codex
对应模块的核心调用链、状态机、事件顺序、错误语义和生命周期，而不只是实现了相似功能。

## 一致性规则

- Codex 源码是主逻辑的唯一事实来源。
- Go 类型可以不同，但职责、输入、输出和状态转换必须等价。
- 平台相关实现允许不同，接口、授权边界和失败语义必须一致。
- 临时教学实现必须标记为“原型”，不能被视为已完成的 Codex 等价实现。
- 每个差异都要写明 Codex 行为、当前行为、偏离原因和补齐计划。

## 当前映射

| 能力 | Codex 源码 | LOB Codex | 状态 | 说明 |
|---|---|---|---|---|
| 协议事件 | `codex-rs/protocol` | `internal/protocol` | 原型 | 当前仅有最小文本流事件，尚未对齐完整协议 |
| Session/Turn | `codex-rs/core/src/session` | `internal/agent` | 未开始 | 下一阶段先还原真实 Session、Turn、Step 边界 |
| 模型客户端 | `codex-rs/core` 模型客户端 | `internal/model` | 原型 | 已接 Responses SSE，尚未对齐请求构造与完整事件映射 |
| 工具路由 | `codex-rs/core/src/tools` | `internal/tools` | 未开始 | 必须复现注册、路由、审批和结果回传流程 |
| 命令执行 | `codex-rs/core/src/exec.rs` | `internal/execution` | 未开始 | 必须保留取消、超时、输出截断和沙箱边界 |
| MCP | `codex-rs/core/src/mcp.rs` | `internal/mcp` | 未开始 | 必须对齐连接、工具刷新、审批和调用语义 |
| App Server | `codex-rs/app-server` | `internal/appserver` | 原型 | 当前 GUI 服务仅用于验证模型链路 |

## 下一步

在增加工具前，先阅读并映射 Codex 的 Session → Turn → Step 主调用链，替换当前简化的
`Runner`。完成条件是事件生命周期、模型请求循环、错误传播和取消行为均能在两边逐项对应。
