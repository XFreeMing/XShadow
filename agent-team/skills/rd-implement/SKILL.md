---
name: rd-implement
description: Implement scoped changes in the task-specified repository with isolated ownership and project-specific checks.
---

Resolve the repository, revision/worktree, scope and acceptance criteria from the current task and linked project resources. Do not assume a default project or copy a previous project's path or architecture. If the target is ambiguous, resolve that before edits. Read the target AGENTS.md, applicable nested instructions, manifests and build/test commands.
Inspect git status and preserve existing user work. Use the assigned worktree or the target repository's supported isolation workflow. Do not assume an isolated checkout includes another checkout's uncommitted changes. One writer owns each branch/PR.
Apply the target project's architecture, API compatibility, data and release rules. These are project context, not universal properties of a language. Make the smallest complete change and verify observable behavior with relevant project checks. Report exact revision, files, commands/results and remaining unverified criteria. Follow the target's evidence requirements for UI changes.
Publishing, releases and external messages follow the user's actual authorization. Do not invent an approval requirement for ordinary local fixes.
