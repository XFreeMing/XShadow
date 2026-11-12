---
name: tech-delivery
description: Implement build pipelines, artifact handling and authorized releases using the target project toolchain.
---

Inspect the repository's actual CI system, build scripts, lockfiles and release instructions; do not assume a vendor. Track source revision, dependency resolution, generated artifact and verification result together. Check artifact contents and the intended target platform, not only the pipeline exit status.
Keep credentials in supported secret stores and avoid leaking them into logs or artifacts. Follow existing versioning/signing/publication rules and the user's authorized release scope. Language-specific compiler and packaging defects are handled with the corresponding stack specialist. Independent release-readiness review belongs to the quality team.
