# LOB Codex 实施计划

## 1. 要实现的 Harness 是什么

Harness 不是一层模型 SDK 包装，而是围绕模型构建的运行时：它接收用户输入，组织上下文，调用模型，识别工具请求，安全执行工具，把结果送回模型，并持续产生可观察、可恢复的事件。

```text
CLI / Web
   ↓
Application Service
   ↓
Agent Runtime ─────→ Session / Context
   ↓                       ↓
Model Adapter ←──── Event Store
   ↓
Tool Router → Approval → Sandbox → Tool Executor
   ↓
MCP / Skills / Plugins / Subagents
```

第一版使用 Go 沿 Codex 的真实调用链实现纵向最小闭环。模块边界、状态流转和事件语义以
Codex 源码为准；Go interface 只承担 Rust trait 的语言等价表达，不用于重新设计主流程。

## 2. Codex 源码阅读地图

参考仓库：OpenAI Codex 源码仓库。

| 学习主题 | Codex 参考位置 | LOB Codex 预定 package |
|---|---|---|
| 协议与事件 | `codex-rs/protocol` | `internal/protocol` |
| 会话与 Turn | `codex-rs/core/src/session` | `internal/session` |
| Agent 执行循环 | `codex-rs/core/src/session/turn.rs` | `internal/agent` |
| 模型调用 | `codex-rs/core` 的模型客户端 | `internal/model` |
| 工具注册与路由 | `codex-rs/core/src/tools` | `internal/tools` |
| 命令执行 | `codex-rs/core/src/exec.rs` | `internal/execution` |
| MCP | `codex-rs/core/src/mcp.rs`、`session/mcp.rs` | `internal/mcp` |
| App Server | `codex-rs/app-server` | `internal/appserver` |
| CLI/TUI | `codex-rs/cli`、`codex-rs/tui` | `cmd/lob-codex` |

阅读时必须回答：入口是什么、输入是什么、状态在哪里、调用顺序是什么、输出事件是什么、
错误和取消如何传播。实现可以使用 Go 语法重写，但这些核心语义不得改变。

## 3. 分阶段实现

### 阶段 0：项目骨架与架构约束

目标：建立 Go 工程、模块边界和决策记录。

任务：

1. 初始化 Go Module、`gofmt`、`go vet` 和最小测试配置。
2. 首轮只建立 `internal/protocol`、`internal/agent`、`internal/model` 和 `cmd/lob-codex`。
3. 定义依赖方向：`cmd` → agent/model → protocol；核心 package 不依赖 CLI。
4. 建立 `examples/`，每阶段保留一个可运行示例。

验收：`go test ./...`、`go vet ./...` 通过，空 CLI 能显示帮助。

### 阶段 1：最小模型对话闭环

目标：从 CLI 输入一句话，流式打印模型文本。

任务：

1. 使用小型 Go interface 定义 `ModelClient` 和供应商无关的请求/响应事件。
2. 实现 OpenAI Responses API 适配器。
3. 支持环境变量配置 endpoint、model、API key。
4. 将流转换为 `response.started`、`text.delta`、`response.completed`。
5. CLI 支持一次性 prompt 和交互模式。

验收：真实模型和 `FakeModel` 都能运行；密钥使用封装配置且不进入日志或事件。

### 阶段 2：Agent Tool Loop

目标：模型可以调用工具，并在一次 Turn 内多步迭代到最终回答。

任务：

1. 定义 `ToolDefinition`、`ToolCall`、`ToolResult`、`ToolExecutor`。
2. 实现 `ToolRegistry`，负责名称查找与参数 Schema 校验。
3. 实现循环：模型响应 → 工具调用 → 执行 → 回传 → 再请求模型。
4. 加入最大 step 数、取消信号和结构化错误。
5. 先实现只读工具：`echo`、`read_file`、`list_files`。

验收：FakeModel 可稳定复现两次工具调用后输出答案；未知工具和非法参数会作为工具结果反馈给模型。

### 阶段 3：事件会话与恢复

目标：会话可回放、重启恢复、分叉和审计。

任务：

1. 设计仅追加事件：session、turn、step、model、tool 生命周期。
2. 实现 JSONL `EventStore`，使用稳定 `sessionId` 与递增 `seq`。
3. 从事件投影 transcript、模型上下文和当前运行状态。
4. 支持中断恢复、损坏尾行容错和会话 fork。
5. 区分事实事件与临时 UI 增量。

验收：杀死进程后可以从最后一个完整事件恢复；同一日志回放得到一致投影。

### 阶段 4：Coding Agent 基础能力

目标：安全地读取、修改和执行工作区代码。

任务：

1. 引入 `Workspace`，所有路径在根目录内解析并防止逃逸。
2. 实现 `grep`、`write_file`、`apply_patch` 和 `exec_command`。
3. 定义只读、工作区写入、危险操作三级权限。
4. 实现 `ApprovalPolicy`，审批请求与决定写入事件。
5. 实现命令超时、输出截断、进程取消。
6. macOS 优先实现 Seatbelt 沙箱；其他平台先安全失败。

验收：路径穿越、符号链接逃逸和未审批危险命令均被阻止；工作区内常规编辑可完成。

### 阶段 5：MCP、Skills 与插件

目标：核心代码不修改也能扩展工具和工作流程。

任务：

1. 实现 stdio MCP 客户端、工具发现和调用。
2. 加入 HTTP MCP 与 OAuth 的接口边界，OAuth 可后置实现。
3. 实现 `AGENTS.md` 分层发现和指令组装。
4. 实现 `SKILL.md` 元数据发现、按需加载和资源解析。
5. 定义 `.lob-plugin/plugin.json`，插件可贡献 Skills、MCP、Hooks 和配置。
6. 插件安装先支持本地路径，再支持 marketplace。

验收：安装一个示例插件后，无需修改核心代码即可出现新工具和新 Skill。

### 阶段 6：长任务与多 Agent

目标：支持复杂、可持续运行的工程任务。

任务：

1. 实现 token 估算、上下文裁剪和持久压缩摘要。
2. 实现 follow-up/inject 输入队列。
3. 子 Agent 使用独立 session，通过受限工具集运行。
4. 实现后台 job、状态查询、输出读取和取消。
5. 实现 goal 状态与计划事件。
6. 加入重试分类、退避、用量和耗时统计。

验收：主 Agent 可委派只读分析，继续处理其他工作，并在稍后收集结果。

### 阶段 7：App Server 与 Web GUI

目标：同一运行时同时服务 CLI 和浏览器界面。

任务：

1. 提取 Application Service，不允许 Web 直接操作 EventStore。
2. 提供 HTTP API 与 SSE/WebSocket 事件流。
3. 实现会话列表、聊天流、工具详情、审批和工作区选择。
4. 增加配置页、模型配置、插件管理和日志诊断。
5. 增加身份认证、并发隔离、速率限制和存储迁移。

验收：CLI 与 Web 打开同一会话时看到一致状态；断线重连不会丢失已提交事件。

## 4. 推荐 Go Module 结构

```text
lob-codex/
├── go.mod
├── cmd/
│   └── lob-codex/
├── internal/
│   ├── agent/
│   ├── appserver/
│   ├── execution/
│   ├── mcp/
│   ├── model/
│   ├── plugins/
│   ├── protocol/
│   ├── session/
│   └── tools/
├── docs/
├── examples/
└── tests/
```

## 5. 每一步的开发节奏

每个阶段固定按以下顺序推进：

1. 定位 Codex 对应入口、协议类型和完整调用链。
2. 在 `docs/CODEX_PARITY.md` 写下 Rust → Go 的模块与类型映射。
3. 等价实现状态机、事件顺序、错误传播和取消语义。
4. 用 FakeModel/FakeTool 验证确定性流程，再接入真实实现。
5. 对照 Codex 补齐失败路径、结构化事件和生命周期边界。
6. 更新一致性清单；未对齐项不得标记完成。

## 6. 第一轮迭代清单

下一次编码从阶段 0 开始，控制在一个小提交内：

- 初始化 Go Module。
- 在 `internal/protocol` 定义 `HarnessEvent`。
- 定义 `EventSink` 与流式 `ModelClient` interface。
- 实现 `FakeModel`。
- 使用 `context.Context`、Channel 和标准库实现仅打印文本增量的 CLI。
- 添加最小单元测试与一个 `examples/01-chat` 示例。

这一轮不实现工具、数据库、Web、MCP 或插件。只有第一条纵向链路稳定后才进入阶段 2。

## 7. 完成定义

项目达到首个可用版本时，应满足：

- 能在真实代码仓库中读取、编辑、运行和验证任务。
- 所有模型和工具行为都有结构化事件记录。
- 危险操作不能绕过审批与沙箱。
- 会话可恢复、可取消、可回放。
- 模型、工具、存储、UI 和插件均可替换。
- CLI 和 Web 共用同一 Harness Core。
