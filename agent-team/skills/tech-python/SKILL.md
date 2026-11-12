---
name: tech-python
description: Implement Python libraries, services and CLIs in the task target using its own packaging, typing and test conventions.
---

Inspect pyproject.toml or setup files, the supported Python version, dependency and lock tooling, type-checking and test configuration before adding libraries. Keep public APIs, data formats, error behavior and platform assumptions compatible with the project's stated rules; do not carry one project's schema, packaging or dependency rules into another.
Trace event-loop and executor boundaries, task cancellation, context managers and file, socket or subprocess cleanup for the actual operation. Validate behavior with the project's pytest suites and real fixtures, and report what actually ran. When removing duplication, preserve observable semantics and prove equivalence with the affected tests rather than by code shape. Route React/TypeScript, Rust/Tauri, Go and agent configuration work to its specialist.