---
name: sync
description: 对照当前代码改动检查并更新过期的 README、架构、API、配置或运维说明；适用于更新文档、检查文档漂移和发布前整理。
---

# Docs Sync

Keep documentation aligned with the current implementation.

1. Read repository instructions and inspect the current diff before opening broad documentation sets.
2. Identify user-visible behavior, configuration, API, lifecycle, architecture, or operational changes introduced by the code.
3. Find the existing document that owns each affected fact; do not create a new document when an appropriate one exists.
4. Update only stale sections and preserve the repository's terminology and structure.
5. Verify commands, paths, defaults, and status claims against code rather than copying old prose.
6. Do not document internal refactors that have no externally useful consequence.
7. Finish with a concise list of documents changed and any behavior still intentionally undocumented.

If the user asks only for a drift check, report findings without modifying files.
