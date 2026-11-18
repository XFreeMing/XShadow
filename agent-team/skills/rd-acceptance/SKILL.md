---
name: rd-acceptance
description: Map approved acceptance criteria to reproducible tests, commands, expected results, versions and evidence, and re-verify the affected scope.
---

Work from the approved acceptance criteria recorded on the issue, not from the implementation or the author's summary. For each criterion produce a verification item: id, precondition and inputs, the command or user path, the expected observable result, the version or revision under test, and where the raw evidence is stored.

Write executable tests in the target repository's existing framework and beside the implementation, at the single most appropriate layer. Reuse an existing test instead of duplicating it when it still covers the changed behavior, and name that test and the reason for reuse. Cover cross-entry effects explicitly: when shared behavior is reached from several entry points, state which entries need re-verification and which are unaffected and why. On a scope, contract or version change, re-verify the affected items and record the reason for any item left unverified. A manual item must state why automation is not possible or not worth the cost, the exact steps, and who performs it.

Run the tests yourself on the candidate version and attach the actual command, output and revision. A plan, a code review, or the author's claim that tests pass never substitutes for the run. Report every item as passed, failed or not run with its evidence location; never mark an item passed because the implementation "should" satisfy it. Hand the mapping and raw evidence to the quality manager, who owns the overall verdict; do not declare overall acceptance yourself.

## In-review step

When a workflow routes `in_review` to this skill, follow [the review entry](references/review-entry.md). Work only from the acceptance criteria and candidate version frozen on the story. Execute the approved acceptance code through the project's supported entry, keep the command, exit code, raw log and artifact, and post one machine-readable attempt record on the original issue. Then obtain the quality manager's independent verdict record; the executor never declares its own run passed.

Run `scripts/review-verdict.mjs` on the platform-exported issue and comments to decide the step. The tool de-duplicates by standard version and attempt identity, and it refuses to count replays, plain comments or run counts, missing evidence, unverified criteria or environment errors as real failures. A fully evidenced pass ends the story with `done`; a failure or unverified result with a next action returns the issue to the delivery team and actually wakes its owner; the same acceptance scope needs at least three distinct, really failing rounds plus the team lead's explicit statement that no executable plan remains before exactly one decision issue is opened for the configured human. Automatic `done` is story acceptance only, never release, merge or daily-upgrade authorization.