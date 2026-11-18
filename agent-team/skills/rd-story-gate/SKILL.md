---
name: rd-story-gate
description: Judge whether a new or replanned user story is ready to start, or return it with a concrete counterexample and a named owner.
---

Judge the issue as written, not the author's explanation of it. A new or formally replanned delivery story receives a start or return verdict before implementation is dispatched. Read the beneficiary and scenario, the intended artifact, the planned delivery or experience entry point, the observable acceptance criteria, the scope, the delivery owner and the stated dependencies.

Start only when each of these holds:
- a concrete, planned entry point exists: where the artifact will be published and how the user will open or use it. The artifact itself need not exist before start; the actual entry, artifact and user access are verified at final acceptance;
- every acceptance criterion is observable by the user, and a verification method for each is stated or derivable: a runnable check, or a manual check that explains why automation is not possible;
- the inputs, environment and access needed to start and to perform that verification are available or explicitly deferred with an owner;
- one delivery owner and one integration target exist for code work, and the stated dependencies are available or genuinely deferred;
- the story delivers an independently usable increment; pure technical layers and step reports do not.

Return the story when a required element is missing, ambiguous or untestable. A return verdict carries at least one concrete counterexample: quote or point to the exact story text or missing required element, state the user-visible consequence, name the responsible person, and give the smallest correction that would make the story startable. Do not rewrite the story, add an approval layer, open an issue per step or re-review a settled story version. Report the verdict with the judged story revision on the original issue.

Simple and report stories stay lightweight: a beneficiary, a concrete report or tool, a planned entry, and one or two checkable criteria are enough. Do not demand the full template, a design document or extra sign-off for them. The verdict judges readiness, not whether the proposed solution is the best option; route solution questions to the requirements analyst or the responsible manager instead.