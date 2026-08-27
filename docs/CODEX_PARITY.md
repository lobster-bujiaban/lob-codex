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
| 协议事件 | `codex-rs/protocol` | `internal/protocol` | 部分对齐 | 已拆分 `ResponseEvent` 与公开 `Event/EventMsg`，并实现文本、剪贴板图片 Message 与工具调用 ResponseItem 子集 |
| Session/Turn | `codex-rs/core/src/session`、`codex-rs/rollout/src/recorder.rs`、`codex-rs/core/src/session/rollout_reconstruction.rs` | `internal/session` | 部分对齐 | 已还原 Submission、后台 loop、RegularTask、Turn/StepContext，以及 Conversation History 的采样、JSONL rollout 写入与恢复主链 |
| 模型客户端 | `codex-rs/core` 模型客户端 | `internal/model` | 部分对齐 | Responses API 现接收 `ResponseItem[]` 并消费文本 Delta、OutputItemDone、Completed；重试和其他 Item 类型尚未对齐 |
| 工具路由 | `codex-rs/core/src/tools` | `internal/tools` | 部分对齐 | 已实现 Tool Definition、Registry/Router、ToolInvocation、echo 与 exec_command 路由 |
| 命令执行 | `codex-rs/core/src/tools/handlers/unified_exec` | `internal/tools` | 部分对齐 | 已实现 TurnEnvironment、审批、Seatbelt、ProcessManager、pipe/PTY、session_id、chunk_id、输出 Delta、TerminalInteraction 与 write_stdin |
| MCP | `codex-rs/core/src/mcp.rs` | `internal/mcp` | 未开始 | 必须对齐连接、工具刷新、审批和调用语义 |
| App Server | `codex-rs/app-server` | `internal/appserver` | 部分对齐 | 已实现 thread start/list/fork、thread_id 路由、独立 Session、workspace/History 恢复及 rollout 运行时流程视图；完整 Codex Turn 元数据尚未实现 |

## Session → Turn → Step 调用链

![Codex Session、Turn、Step 主链流程图](./images/session-turn-step.png)

可编辑源图位于 [`diagrams/session-turn-step.svg`](./diagrams/session-turn-step.svg)。

| 顺序 | Codex | LOB Codex |
|---|---|---|
| 1 | `SessionIo::submit` 创建 `Submission` | `IO.Submit` 创建 `Submission` |
| 2 | `submission_loop` 分派 `Op` | `submissionLoop` 分派 `Op` |
| 3 | `turn_input::handle` 决定 start/steer/reject | `handleTurnInput` 空闲时 start，运行中将 steer 加入当前任务输入队列；Interrupt 经 submission loop 取消任务 |
| 4 | `Session::spawn_task/start_task` 启动后台任务 | `spawnRegularTask` 启动可取消 goroutine |
| 5 | `RegularTask::run` 发送 `TurnStarted` | `runRegularTask` 发送 `TurnStarted` |
| 6 | `run_turn` 捕获 `StepContext` 并循环采样 | `runTurn` 捕获 `StepContext` 并保留 follow-up 循环 |
| 7 | `run_sampling_request` 消费 `ResponseEvent` | `runSamplingRequest` 消费内部 `ResponseEvent` |
| 8 | `on_task_finished` 发送终态事件 | `onTaskFinished` 发送 TurnComplete/TurnAborted |

## Conversation History 调用链

![Codex Conversation History 主链流程图](./images/conversation-history.png)

可编辑源图位于 [`diagrams/conversation-history.svg`](./diagrams/conversation-history.svg)。

| 顺序 | Codex | LOB Codex |
|---|---|---|
| 1 | `record_pending_input` 将 Turn 输入转换并记录为 `ResponseItem` | `runTurn` 将 `TurnInput` 转为用户 `ResponseItem` 后调用 `RecordItems` |
| 2 | `clone_history().for_prompt(...)` 返回当前 Step 的模型输入快照 | `ConversationHistory.ForPrompt()` 返回隔离切片 |
| 3 | Model Client 接收 `Vec<ResponseItem>` | `model.Request.Input` 接收 `[]protocol.ResponseItem` |
| 4 | `ResponseEvent::OutputItemDone` 交给输出处理并记录 | `ResponseOutputItemDone` 到达时立即回写 Conversation History |
| 5 | 下一 Turn 再次从完整 History 构造 prompt | 长生命周期 Session 保留历史并向下一次请求发送完整数组 |
| 6 | `RolloutRecorder` 将 canonical item 写为带 ordinal 的 JSONL | 每次 `RecordItems` 后追加 `timestamp/ordinal/type/payload` rollout line |
| 7 | resume replay `RolloutItem::ResponseItem` 重建 ContextManager | Session 懒加载时回放 `response_item`，同时供历史页面读取 |
| 8 | `for_prompt` 标准化调用与输出配对 | 为中断调用临时插入 `aborted` 输出并移除孤立输出，不污染 rollout |

## Tool Router → Agent Tool Loop

![Codex Tool Router 与 Agent Tool Loop 流程图](./images/tool-router-loop.png)

可编辑源图位于 [`diagrams/tool-router-loop.svg`](./diagrams/tool-router-loop.svg)。

| 顺序 | Codex | LOB Codex |
|---|---|---|
| 1 | StepContext 持有本 Step 的 ToolRouter 与模型可见 ToolSpec | `captureStepContext` 捕获 Router，请求携带 `[]tools.Definition` |
| 2 | `OutputItemDone(FunctionCall)` 交给 `ToolRouter::build_tool_call` | `BuildToolCall` 将 FunctionCall ResponseItem 转为内部 Call |
| 3 | 原始调用先记录，再交给工具 Runtime | FunctionCall 先写 History，再由 Router 查找并执行 echo |
| 4 | 参数或路由错误转换为给模型的 FunctionCallOutput | 未知工具与参数错误均生成模型可见输出，不直接终止 Turn |
| 5 | 工具结果写回 History，并设置 `needs_follow_up` | FunctionCallOutput 写回 History，下一 Step 从完整历史继续采样 |
| 6 | 客户端接收工具调用生命周期事件 | App Server 以 NDJSON 推送 started/completed，GUI 显示工具卡片 |

## TurnEnvironment → Read-only exec_command

![Codex unified exec 只读执行边界图](./images/unified-exec-readonly.png)

可编辑源图位于 [`diagrams/unified-exec-readonly.svg`](./diagrams/unified-exec-readonly.svg)。

Codex Core 不提供本地 `read_file` / `list_files` 独立工具；本地文件读取通过
`exec_command` 完成，外部 MCP Server 可以另外提供同名 filesystem 工具。LOB Codex 因此没有
自行发明文件工具，而是实现 unified exec 的第一个安全切片。

| 顺序 | Codex | LOB Codex |
|---|---|---|
| 1 | ToolInvocation 携带 Call、StepContext 与 TurnEnvironment | Invocation 携带 Call、WorkingDirectory 与 WorkspaceRoot |
| 2 | Handler 解析 cmd/workdir/timeout 等参数 | ExecCommandExecutor 使用同名参数子集 |
| 3 | ExecPolicy 决定允许、拒绝或申请审批 | 当前只读 allowlist 自动允许，其余返回“requires approval”模型结果 |
| 4 | UnifiedExecRuntime 编排审批、Sandbox 与 ProcessManager | 当前直接进入 macOS Seatbelt，只读工作区且禁止写入和网络 |
| 5 | ExecCommandToolOutput 截断后转 FunctionCallOutput | 返回 exit_code、wall_time_seconds、output 与截断标记 |

GUI 启动环境可能没有交互式 Shell 的 PATH。执行器会保留进程 PATH，并补充 Homebrew、`/usr/local`
及 Codex/ChatGPT bundled resources；对应目录只开放 Seatbelt 读取权限，使 `rg` 等 bundled tool 的
行为与原生 Codex 桌面端一致。

## Exec Approval 暂停与恢复

![Codex Exec Approval 暂停与恢复流程图](./images/approval-sandbox-loop.png)

可编辑源图位于 [`diagrams/approval-sandbox-loop.svg`](./diagrams/approval-sandbox-loop.svg)。

| 顺序 | Codex | LOB Codex |
|---|---|---|
| 1 | ExecPolicy 产生 `NeedsApproval` | 非只读命令产生 ApprovalRequest |
| 2 | 先注册 `call_id + approval_id` oneshot，再发送事件 | 先写入 pending map，再发送 ExecApprovalRequest |
| 3 | Turn 等待 ReviewDecision，同时允许 submission loop 接收 Op | 工具 goroutine 等待 channel，submission loop 继续接收审批 Op |
| 4 | Approved 进入 Sandbox；Denied 作为可恢复拒绝 | 批准一次开放工作区写权限但仍禁网；拒绝生成 FunctionCallOutput |
| 5 | 取消时清理 pending approval 并中止等待 | 请求取消删除 pending map，工具返回 context cancellation |

## UnifiedExec ProcessManager

![Codex UnifiedExec ProcessManager 流程图](./images/unified-exec-process-manager.png)

可编辑源图位于 [`diagrams/unified-exec-process-manager.svg`](./diagrams/unified-exec-process-manager.svg)。

| 顺序 | Codex | LOB Codex |
|---|---|---|
| 1 | ProcessManager 启动进程并注册 process ID | Session 级 ProcessManager 分配递增 session_id |
| 2 | 等待到 yield deadline 或进程退出 | 250–30000ms clamp；提前完成直接返回 exit_code |
| 3 | 未完成时返回 session_id 并保留 ProcessEntry | 返回 session_id、当前增量输出和 wall time |
| 4 | write_stdin 按 process ID 串行写入或轮询 | 每个 managedProcess 使用 interaction mutex |
| 5 | 每次读取并消费新增输出 | readOffset 保证后续只返回上次之后的输出 |
| 6 | 观察到退出后从 store 清理 | 返回 exit_code 后删除；Session Close 强制杀死剩余进程 |

## UnifiedExec PTY

![Codex UnifiedExec PTY 流程图](./images/unified-exec-pty.png)

可编辑源图位于 [`diagrams/unified-exec-pty.svg`](./diagrams/unified-exec-pty.svg)。

| 顺序 | Codex | LOB Codex |
|---|---|---|
| 1 | ProcessHandle 统一包装本地 PTY 与其他执行器 | managedProcess 统一包装 pipe 与 creack/pty 主端 |
| 2 | `tty: true` 创建终端会话并绑定子进程 | 创建 24×120 PTY，sandbox-exec/zsh 成为终端前台进程 |
| 3 | PTY 输出进入共享输出缓冲 | io.Copy 写入 synchronizedBuffer，沿用增量 readOffset |
| 4 | write_stdin 写 PTY writer | 普通字符写主端，`` 写终端 Ctrl-C 控制字符 |
| 5 | PTY 退出后关闭主端并等待输出收尾 | Wait → close PTY → output copy 完成 → close done |
| 6 | 客户端看到终端属性 | FunctionCallOutput 带 `tty: true`，GUI 显示 TTY 标记 |

## UnifiedExec 终端生命周期事件

![Codex UnifiedExec 终端生命周期事件流程图](./images/unified-exec-terminal-events.png)

可编辑源图位于 [`diagrams/unified-exec-terminal-events.svg`](./diagrams/unified-exec-terminal-events.svg)。

| 顺序 | Codex | LOB Codex |
|---|---|---|
| 1 | exec reader 读取 stdout/stderr 字节块 | pipe 分流 stdout/stderr，PTY 统一视为 stdout |
| 2 | 读取时发送 `ExecCommandOutputDelta` | Process output writer 在写入缓冲后发送同名事件 |
| 3 | exec/write_stdin 每次收集生成新 chunk id | 每次结果生成独立 `chunk_id` |
| 4 | write_stdin 完成写入与轮询后判断进程状态 | 串行交互后判断 running/exited |
| 5 | 非空输入或进程仍存活时发送 `TerminalInteraction` | 使用原 exec call_id、process_id 和 stdin 发送事件 |
| 6 | 工具结果随后回写模型历史 | 生命周期事件先到客户端，ToolCallCompleted 随后到达 |

Codex 当前 `write_stdin` schema 只有 `session_id/chars/yield_time_ms/max_output_tokens`，没有
PTY `rows/cols` resize 参数。Codex TUI resize 用于界面重排，不属于 unified exec 子进程协议，
因此 LOB Codex 保持固定 24×120 PTY，而不自行扩展模型工具参数。

## UnifiedExec Head-Tail 输出收集

![Codex UnifiedExec Head-Tail 输出收集流程图](./images/unified-exec-head-tail-buffer.png)

可编辑源图位于 [`diagrams/unified-exec-head-tail-buffer.svg`](./diagrams/unified-exec-head-tail-buffer.svg)。

| 顺序 | Codex | LOB Codex |
|---|---|---|
| 1 | transcript 使用 1 MiB `HeadTailBuffer` | Session 进程输出缓冲固定上限 1 MiB |
| 2 | 容量按 50% head / 50% tail 分配 | 稳定保留前缀与最新后缀，丢弃中间字节 |
| 3 | 每次收集 drain 当前 transcript | exec/write_stdin 每次只消费上次之后的输出 |
| 4 | 按 max output token budget 再限制结果 | `max_output_tokens × 4` 形成模型可见字节预算 |
| 5 | 中间插入 `... N bytes omitted ...` | 使用相同省略标记并返回省略字节数 |
| 6 | 原始字节数按 4 bytes/token 估算 | 向上取整返回 `original_token_count` |
| 7 | 每个 call 最多 10000 个输出 Delta | 同一 exec call 共享计数，每块最多 8192 字节 |

## Thread → Session → Workspace

![Codex Thread、Session 与 Workspace 路由流程图](./images/thread-session-workspace.png)

可编辑源图位于 [`diagrams/thread-session-workspace.svg`](./diagrams/thread-session-workspace.svg)。

| 顺序 | Codex | LOB Codex |
|---|---|---|
| 1 | `thread/start` 接收可选 cwd | `POST /api/threads` 必须接收 workspace_root |
| 2 | Thread Manager 创建 thread id 与 Session | Handler 创建 thread metadata，Session 首次使用时懒加载 |
| 3 | Session config 持有规范化 cwd | 解析绝对路径与 symlink，拒绝不存在或非目录路径 |
| 4 | Turn 从 Session 捕获环境快照 | Router 的 TurnEnvironment 固定为 thread workspace |
| 5 | App Server 按 thread id 路由 turn/approval | chat 与 approval 都定位同一个 thread-owned Session |
| 6 | `thread/resume` 恢复 rollout 与 cwd | 启动时加载 metadata，Session 懒加载时从同 thread JSONL 恢复模型历史与页面消息 |
| 7 | 多 thread 可分别运行 | 每个 thread 独立 chat mutex、history、审批和进程存储 |
| 8 | `thread/fork` 从指定 rollout 位置创建新 thread | 消息工具栏按 ResponseItem 数截断历史，新 thread 继承 workspace 并恢复该前缀 |
| 9 | App Server 管理项目与 thread 生命周期 | 侧栏按 Codex 项目/对话层级交互，首条用户消息生成标题，支持当前项目新对话及受保护的磁盘目录删除 |

浏览器无法通过普通目录上传控件获得本机绝对路径，因此本地 App Server 在 macOS 使用系统
文件夹选择器取得路径，再复用相同的 workspace 校验与 `thread/start` 主链；其他平台当前保留
明确的“不支持原生选择器”错误，仍可手动输入绝对路径。

工作区删除属于 LOB Codex 本地管理扩展：只允许删除已登记的非默认工作区，先关闭其全部
Session，再删除项目目录与对应 thread metadata/rollout；根目录、用户主目录和当前默认工作区受保护。

## ExecPolicy 与 Session Prefix Rule

![Codex ExecPolicy 与 Session Prefix Rule 流程图](./images/exec-policy-prefix-rule.png)

可编辑源图位于 [`diagrams/exec-policy-prefix-rule.svg`](./diagrams/exec-policy-prefix-rule.svg)。

| 顺序 | Codex | LOB Codex |
|---|---|---|
| 1 | Shell 命令解析为策略可检查的 argv | 解析引号与转义，并拆分由 `&&`、`||`、`|`、`;` 连接的纯命令序列 |
| 2 | Policy 检查内置规则与当前环境规则 | 先检查内置只读命令，再检查 Session prefix rules |
| 3 | 未匹配时生成 ExecApprovalRequirement | 返回 NeedsApproval、原因和可选 ProposedPrefix |
| 4 | ApprovedForSession 写入 session approval cache | 将 argv token prefix 写入 Router 所属 Session 内存策略 |
| 5 | 后续命令按 token 前缀匹配 | 使用切片逐 token 比较，不使用字符串 HasPrefix |
| 6 | Session 结束清空缓存 | ExecPolicy 随 Session Router 销毁，不写入磁盘 |

## 持久化 ExecPolicy Amendment

![Codex 持久化 ExecPolicy Amendment 流程图](./images/exec-policy-amendment.png)

可编辑源图位于 [`diagrams/exec-policy-amendment.svg`](./diagrams/exec-policy-amendment.svg)。

| 顺序 | Codex | LOB Codex |
|---|---|---|
| 1 | 审批请求携带 proposed execpolicy amendment | 简单命令携带结构化 argv prefix |
| 2 | 客户端返回 `ApprovedExecpolicyAmendment` | GUI 返回 `approved_with_amendment` |
| 3 | Core 将 amendment 写入规则层 | ExecPolicy 原子写入 `tmp/exec-policy.rules` |
| 4 | 新策略评估可命中持久化规则 | 新 Session 启动时加载并按 token 前缀匹配 |
| 5 | 命中后无需再次审批 | 返回 `persistent prefix` 并进入可写 Seatbelt |

项目运行数据统一以 `<workspace>/tmp/` 为根目录，并由 Git 忽略。当前仅有 ExecPolicy 规则；
后续 rollout、会话索引等本地数据也沿用该边界，不散落到源码目录。

## 当前明确差异

- `TurnInputMode::StartOrSteer` 已实现空闲启动和运行中 input queue；引导在当前 Step 收尾后写入 History 并触发下一 Step。Interrupt 经 submission loop 取消当前任务。当前尚未实现采样过程中抢占并立刻重启 Step。
- `ResponseItem` 当前实现文本与 base64 `input_image` Message、FunctionCall 和 FunctionCallOutput；Reasoning、远程/本地图片预处理及音频内容尚未补齐。
- Conversation History 已实现调用/输出配对标准化及 ResponseItem 子集的 rollout 持久化与恢复；Codex 的完整媒体标准化、截断策略和 token 统计尚未实现。
- Tool Router 当前按顺序执行；并行工具尚未实现。
- PTY 当前使用固定 24×120 尺寸；Codex unified exec 同样未暴露 resize 工具参数，平台远程执行器尚未实现。
- 已实现 chunk_id、输出 Delta、TerminalInteraction、1 MiB head-tail buffer、Delta 数量/大小上限和省略字节统计。
- 审批支持 approved、approved_for_session、approved_with_amendment 与 denied；持久化规则首版采用项目 `tmp/` JSON 文件，尚未实现 Codex 正式规则语法。
- Prefix rule 第一版只缓存简单单命令；复合 shell 中所有子命令均为内置只读命令时自动允许，否则只允许批准一次。
- Seatbelt 不支持在 Codex 自身 Seatbelt 环境中嵌套启动；嵌套失败会作为普通工具输出回给模型。
- Rollout 当前只持久化 `response_item`，Codex 的 SessionMeta、TurnContext、EventMsg、hooks、compaction、token 状态和启动预热尚未实现。
- 历史页面由 canonical ResponseItem 重建消息与工具卡片；尚未恢复 Codex 完整 Turn 状态、审批卡片和终端增量事件。
- Fork 已支持历史前缀与 workspace 继承，尚未持久化 Codex 的 `forked_from_id` 和 ordinal lineage 元数据。
- 运行时流程视图从 canonical ResponseItem 重建 Turn、Step、工具调用和汇总指标；精确 token usage、审批与终端事件需等待对应 RolloutItem 补齐。
- 协议目前只覆盖这条最小调用链所需事件，字段也未覆盖全部 Codex 元数据。

## 下一步

继续实现 unified exec 远程执行器抽象；随后补齐 rollout 的 SessionMeta、TurnContext 与完整 Turn
重建语义。
