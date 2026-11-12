---
name: tech-web-testing
description: Implement Vitest, Testing Library and Playwright tests for web behavior and supported Electron automation.
---

Select the lowest layer that observes the requested behavior: pure logic, component interaction, or actual browser journey. Use the target's fixture and test-data helpers. Prefer user-visible roles and behavior to internal component structure. Synchronize on observable conditions; investigate flakiness rather than masking it with arbitrary sleeps.
Use isolated data and deterministic external-service boundaries. For a regression, demonstrate the original failure when practical and confirm the fix. Do not silently substitute browser testing for native desktop acceptance. Go/Rust/Python unit tests belong to specialists in those stacks. Test strategy and independent acceptance stay with Grok or Codex according to difficulty.
