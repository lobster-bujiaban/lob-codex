---
name: review
description: 审查当前代码改动中的正确性、回归、不安全行为和验证缺口；适用于代码审查、检查差异、提交前找问题等场景。
---

# Code Review

Review the current working-tree changes as a senior maintainer.

1. Read the repository instructions and inspect `git status` plus the relevant diff.
2. Trace each changed behavior through its callers and consumers before judging it.
3. Prioritize concrete correctness bugs, regressions, data loss, security boundaries, concurrency issues, and broken lifecycle semantics.
4. Check whether validation covers the risky behavior, but do not request broad tests without a specific risk.
5. Report findings ordered by severity. Include the exact file and tight line range, the triggering scenario, and the expected behavior.
6. Do not report style preferences, speculative concerns without a reproducible path, or issues unrelated to the diff.
7. If there are no actionable findings, say so directly and mention only material residual risks or unverified behavior.

Do not modify files unless the user explicitly asks for fixes.
