---
name: map
description: 通过入口、模块边界、数据流和生命周期调用链理解陌生仓库；适用于项目结构、架构分析、调用链梳理和新人阅读指引。
---

# Project Map

Build an evidence-backed map of the current repository.

1. Read repository instructions and identify the build system, executable entry points, and primary packages.
2. Trace two or three important runtime paths from their entry points through core modules to external boundaries.
3. Distinguish source-of-truth modules from adapters, generated files, examples, and runtime data.
4. Cite concrete files and symbols for every architectural claim.
5. Explain where a new maintainer should start, what can be ignored initially, and which boundaries are risky to change.
6. Prefer a compact module table and short call-chain lists. Do not dump the directory tree.

Stay read-only unless the user explicitly asks for changes.
