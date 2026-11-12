---
name: tech-rust-tauri
description: Implement Rust and Tauri 2 code, including commands, capabilities, IPC and process lifecycle.
---

Inspect Cargo manifests, edition and rust-version, feature flags and the Tauri 2 configuration before changing code. Model ownership and borrowing deliberately, keep error types and Result boundaries explicit, and preserve the target's public API and data compatibility rules rather than assuming another project's conventions.
Trace Tokio runtime use, cancellation, task shutdown and subprocess or resource termination on the real path. For Tauri, verify the command surface, capabilities and permissions, IPC payloads and the frontend bridge, and exercise the affected native path instead of a browser render alone. Keep React interface work with the React specialist and Electron with its own specialist. Adopt patterns or abstractions only where the current responsibility requires them.