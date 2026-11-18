---
name: rd-acceptance
description: Map approved acceptance criteria to reproducible tests, commands, expected results, versions and evidence, and re-verify the affected scope.
---

Work from the approved acceptance criteria recorded on the issue, not from the implementation or the author's summary. For each criterion produce a verification item: id, precondition and inputs, the command or user path, the expected observable result, the version or revision under test, and where the raw evidence is stored.

Write executable tests in the target repository's existing framework and beside the implementation, at the single most appropriate layer. Reuse an existing test instead of duplicating it when it still covers the changed behavior, and name that test and the reason for reuse. Cover cross-entry effects explicitly: when shared behavior is reached from several entry points, state which entries need re-verification and which are unaffected and why. On a scope, contract or version change, re-verify the affected items and record the reason for any item left unverified. A manual item must state why automation is not possible or not worth the cost, the exact steps, and who performs it.

Run the tests yourself on the candidate version and attach the actual command, output and revision. A plan, a code review, or the author's claim that tests pass never substitutes for the run. Report every item as passed, failed or not run with its evidence location; never mark an item passed because the implementation "should" satisfy it. Hand the mapping and raw evidence to the quality manager, who owns the overall verdict; do not declare overall acceptance yourself.