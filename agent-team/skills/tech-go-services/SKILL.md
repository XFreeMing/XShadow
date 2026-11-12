---
name: tech-go-services
description: Implement Go services, concurrent workers, API boundaries and SQL integration in the task target.
---

Inspect go.mod, service boundaries and repository conventions before choosing APIs or packages. Trace context cancellation, goroutine ownership, channel closure, connection cleanup and transaction scope for the actual operation. Review error propagation and idempotency where retries really occur.
Keep transport parsing and domain behavior separate according to the project's existing design. Check database engine behavior and migration instructions rather than importing another repository's SQL rules. Validate concurrency with a relevant behavior test or race check when the changed path warrants it. Report actual fixture isolation and external dependencies. Route non-Go implementation to the appropriate specialist.
