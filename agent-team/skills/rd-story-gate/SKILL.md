---
name: rd-story-gate
description: Judge whether a new or replanned user story is ready to start, or return it with a concrete counterexample and a named owner.
---

Judge the issue as written, not the author's explanation of it. A new or formally replanned delivery story receives a start or return verdict before implementation is dispatched. Read the beneficiary and scenario, the concrete artifact, the delivery or experience entry point, the observable acceptance criteria, the scope, the delivery owner and the stated dependencies.

Start only when each of these holds:
- a real user can obtain the promised artifact or exercise the feature through the named entry point, and the entry is reachable from the environment the story names;
- every acceptance criterion is observable by the user and can map to a runnable check or a manual check that explains why automation is not possible;
- one delivery owner and one integration target exist for code work, and the stated dependencies are available or genuinely deferred;
- the story delivers an independently usable increment; pure technical layers and step reports do not.

Return the story when a required element is missing, ambiguous or untestable. A return verdict carries at least one concrete counterexample: quote or point to the exact story text or absent artifact, state the user-visible consequence, name the responsible person, and give the smallest correction that would make the story startable. Do not rewrite the story, add an approval layer, open an issue per step or re-review a settled story version. Report the verdict with the judged story revision on the original issue.

Simple and report stories stay lightweight: a beneficiary, a concrete report or tool, a usable entry, and one or two checkable criteria are enough. Do not demand the full template, a design document or extra sign-off for them. The verdict judges readiness, not whether the proposed solution is the best option; route solution questions to the requirements analyst or the responsible manager instead.