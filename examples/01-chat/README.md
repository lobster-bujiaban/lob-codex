# 01 - 最小流式对话

这个示例展示第一条 Harness 纵向链路：CLI 把输入交给 Agent Runner，Runner
调用 FakeModel，再把模型事件流转发给终端。

```bash
go run ./cmd/lob-codex "你好，LOB Codex"
```

预期输出：

```text
Fake model: 你好，LOB Codex
```
