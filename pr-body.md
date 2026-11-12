## Problem

XShadow has no first-class goal object. Work is captured as issues (`issue`) under projects, and the existing parent/child, stage and `issue_dependency` semantics belong to tasks. A project is an organization/access boundary, and `ContactSalesInquiry.Goals` is only a sales-form field. As a result, a team has nowhere to record a deliverable's intent, scope, acceptance criteria, owner and lifecycle as one object, and no way to attach existing tasks to that deliverable without repurposing an issue (or a parent issue) as a pseudo-goal.

DEV-5 is the first independently usable increment of DEV-4: manage goals from the workspace and from a project, and link/unlink existing tasks to a goal. One-sentence plan generation and dependency-driven execution are explicitly out of scope and remain in DEV-6/DEV-7.

## Goal

After this change:

- A workspace member can create, view, edit and delete a goal from the Work area and from a project. A goal carries title, intent, scope, acceptance criteria, owner (member / agent / squad, or none), an optional project (absent means a workspace-level goal) and an independent lifecycle status: `planned`, `in_progress`, `paused`, `completed`, `cancelled`.
- Existing tasks can be linked to and unlinked from a goal; the goal detail lists its linked tasks. Linking does not change the task type, does not turn a task into a goal, and does not alter `parent_issue_id`, stage, dependencies, owner or status.
- Goal completion is explicit only. Linked tasks becoming `done`, `cancelled` or `in_review` — including all of them — never change the goal status. The owner field is an assignment: it grants no access and starts no agent run.
- The same shared pages work on Web and Desktop. Mobile receives no new UI, and the existing task API remains parse-compatible for installed clients.
- UI copy is complete in en, es, fr, ja, ko, ru, zh-Hans and zh-Hant.

## Proposal

### Data and domain rules

- New `goal` and `goal_issue` tables (migrations 649–657). No foreign keys or cascades (repository rule); validation and cleanup live in application code. Every index, including primary-key backing indexes, is created in its own single-statement `CREATE [UNIQUE] INDEX CONCURRENTLY` migration and registered with the migration runner's cleanup lists. sqlc models/queries are generated.
- `goal_issue` has no status of its own. Its `(workspace_id, issue_id)` primary key enforces one goal per task; a goal has 0..N linked tasks.
- Links are idempotent: repeated link or unlink succeeds. A task already linked to another goal returns 409 `issue_already_linked`; a task and a goal must be in the same workspace and the same project (including both project-less) or 409 `goal_project_mismatch`; moving a goal that still has links to another project returns 409 `goal_has_linked_issues` before writing. The goal status only changes through an explicit goal write.

### Access control

Goal routes live in the existing workspace-member route group and reuse the private-project and agent-initiator visibility rules. Invisible goals, projects and tasks return 404 and are excluded from `total`; private-project membership changes immediately affect goal reads. Goal responses and the issue list use the existing JSON error envelope and issue response type.

### Cleanup and atomicity

- Task deletion (single and batch), project deletion and workspace deletion remove the relevant links/targets in the same transaction; deleting a task or project does not delete unrelated tasks or dependencies.
- The shared `UpdateIssue` SQL write boundary unlinks a task when its `project_id` actually changes. This covers single update, batch update, drag and `service.UpdateContent`; ordinary edits and same-project moves keep the link, and a rejected revision does not unlink.
- No dangling `goal_issue` rows remain after task/project/workspace deletion or a project-changing move.

### API

Workspace-scoped endpoints under `/api/goals`: list, create, get, update (PUT, partial), delete, list linked issues, link and unlink. Goal responses are parsed through zod + `parseWithFallback`; unknown goal status is kept as unknown rather than interpreted as completed, and a response without a valid `id` fails the save instead of navigating to an empty goal id.

### Shared UI and navigation

- `packages/core/goals`: schema-backed types, API client methods, workspace-scoped query keys/options and mutations; no new Zustand server-data store.
- `packages/views/goals`: goal list, create/edit form, detail, linked-task dialog, owner/project pickers and project goals tab. The Work panel gets a Goals entry; a project page gets a `?tab=goals` view.
- Web adds thin `/[workspaceSlug]/goals` and `/[workspaceSlug]/goals/[id]` routes under the dashboard layout. Desktop adds sibling routes plus a thin detail wrapper; both reuse the shared pages and `DashboardGuard`.
- After a terminal 400/403/404 on a previously loaded goal detail, the shared page stops showing the stale title/body and the breadcrumb falls back to the localized `Goal` label; tab and window titles also fall back. Offline and 5xx keep the last known title and offer retry. Reconnect invalidation includes the goals prefix.
- Editing a project goal to workspace scope shows the existing eight-locale `detail.project_moved_scope` warning before save; it disappears when the original project is selected again, Cancel does not write, and a goal with linked tasks keeps the project selector locked.

### Realtime

A new `goal:changed` event with an empty payload is published after goal writes, link/unlink, project deletion and project visibility/membership changes. Clients invalidate goal queries and re-read through the HTTP API, so the broadcast never carries a goal a viewer cannot see. Old clients ignore the unknown event.

## Alternatives (optional)

- **Add `goal_id` to `issue`.** Rejected: it expands the high-frequency `IssueResponse`, projections and old-client parsing surface, and couples link writes to the goal lifecycle. A separate `goal_issue` relation keeps existing task APIs unchanged.
- **Use a parent issue or `issue_dependency` as the goal.** Rejected: it overloads task semantics (status, stage, dependencies, automatic dispatch) and contradicts the requirement that a goal is an independent object.
- **Allow arbitrary many-to-many or primary/secondary goals.** Rejected for this increment: no authorized requirement, and one goal per task keeps the contract and UI simple.

## Screenshots

> **Attachment upload is pending.** This run could not authenticate to GitHub's attachment endpoint, so no screenshots are embedded yet. The files below exist as DEV-5 comment attachments and are reproduced from the stated source SHAs. After the draft PR exists, upload them with `scripts/pr-shots` (see CONTRIBUTING.md#screenshots-and-recordings) and move the generated blocks under this heading. Do not use Multica `localhost` URLs in this PR.

### Web / shared UI

The initial 11 captures (`02-goal-create-filled.png` through `18-goal-not-found.png` in the table) come from the initial Web delivery `9b0f13cfc`. The four fix-verification captures (`scope-hint.png`, `scope-hint-reverted.png`, `member-before.png`, `member-after.png`) come from `68bd8a112bcde89659bfede5ba731cdbc18ac122`. The frontend product tree at that latter SHA is unchanged at integration SHA `411a122e`; this does not relabel the earlier captures as new runs.

| File | What it shows |
| --- | --- |
| `02-goal-create-filled.png` | Create-goal dialog filled with title, intent, scope, acceptance criteria, project, owner and status. |
| `14-goals-list.png` | Workspace Goals list entry. |
| `12-project-goals-tab.png` | Project page `?tab=goals` with a project goal. |
| `04-goal-task-linked.png` | Goal detail with a linked task. |
| `05-goal-task-unlinked.png` | The same goal after unlinking. |
| `07-goal-complete-confirm.png` | Explicit completion confirmation. |
| `19-goal-completed-clean.png` | Completed state; linked task status did not drive it. |
| `15-member-goal-access.png` | Second-client member access to a goal. |
| `16-member-permission-revoked.png` | Member view after workspace membership removal (initial Web run). |
| `17-reconnect-refresh.png` | Detail after reconnect refresh. |
| `18-goal-not-found.png` | Unknown/inaccessible goal state. |
| `scope-hint.png` | Pre-save workspace-scope hint when a project goal is switched to Workspace. |
| `scope-hint-reverted.png` | The hint disappears when the original project is selected again. |
| `member-before.png` | React-fix verification: private-project goal loaded by a member. |
| `member-after.png` | React-fix verification: membership revoked, detail cleared without manual refresh; breadcrumb falls back to `Goals > Goal`. |

### Desktop (Linux X11, Electron 44.9.3 / Chromium 152.0.7977.167) — source SHA `ce63b9cdaef8b2070233385890180ab801ceed4b`

This evidence predates the shared stale-detail fix (`68bd8a112`); final Electron re-verification at `411a122e` is still pending in DEV-5.

| File | What it shows |
| --- | --- |
| `b1-project-goals-tab.png` | Desktop project `Goals` tab. |
| `c1-goal-linked-two-tasks.png` | Desktop goal detail with two linked tasks. |
| `c3-private-goal-before-revoke.png` | Desktop private-project goal loaded before membership revocation. |
| `c4-private-goal-after-revoke.png` | Desktop after revocation. Note: this capture predates the shared fix, so the breadcrumb still shows the old goal name; the tab/window title already falls back to `Goal`. Final verification must confirm the breadcrumb cleanup. |
| `c6-goal-after-reconnect.png` | Desktop after reconnect; renamed goal refreshed in tab and detail. |
| `session-c-final.mp4` | Desktop final-session recording (21.3 s). |

## Review guide

- **Server**: `server/internal/handler/goal.go`, `server/pkg/db/queries/goal.sql`, `server/migrations/649-657_*`, and the cleanup changes in `server/internal/handler/issue.go`, `project.go`, `project_member.go`, `workspace.go`, `server/pkg/db/queries/issue.sql` and `workspace_delete.sql`.
- **Core**: `packages/core/api/schemas.ts`, `client.ts`, and `packages/core/goals/`.
- **Views**: `packages/views/goals/`, `packages/views/projects/components/project-detail.tsx`, `packages/views/layout/tab-presentation.tsx`, and the eight `packages/views/locales/*/goals.json` files.
- **Apps**: `apps/web/app/[workspaceSlug]/(dashboard)/goals/`, `apps/desktop/src/renderer/src/routes.tsx` and `apps/desktop/src/renderer/src/pages/goal-detail-page.tsx`.
- **Tests**: `server/internal/handler/goal_test.go`, `goal_workspace_delete_test.go`, `packages/core/goals/*.test.ts(x)`, `packages/views/goals/**/*.test.tsx`.

## Commits

Product commits (on top of the inherited baseline commit):

- `891fa8735` feat(server): add goal objects and issue links
- `fcd7fe24c` fix(server): use PUT for goal updates per API contract
- `07a743d54` feat(goals): add goal management across core, views and web
- `51e74da94` feat(desktop): add goal routes and tab presentation
- `6a1f28355` fix(goals): clear stale detail on lost access and warn before workspace scope
- `ac4670e67` fix(server): require paired owner fields on goal updates
- `411a122e0` test(server): cover goal cleanup on workspace deletion

## Verification

Results below are the actual run records from the DEV-5 delivery. This PR-preparation step did not re-run them; it did verify the integrated product trees are identical to the delivering SHAs.

| Check | Where | Result |
| --- | --- | --- |
| `go test ./cmd/migrate ./internal/migrations -count=1` | integration SHA `411a122e` | PASS |
| `go test ./internal/handler -run TestGoal -count=1 -v` | Go author, isolated DB, `ebf9c88e3` | 13 target tests PASS |
| `go test -race ./internal/handler -run TestGoal -count=1` | Go author, isolated DB, `ebf9c88e3` | PASS (1.242 s) |
| `go build ./cmd/server`, `go vet ./internal/handler ./cmd/server` | Go author, `ebf9c88e3` | PASS |
| `bash scripts/test-go.sh --race` | Go author, earlier SHA `6038b31f` | PASS; not re-run at `411a122e` |
| Migrations 649–657 down/up on a disposable DB | Go author, explicit disposable DB | Goal domain down/up PASS; whole-DB down stops at the existing migration 494 forward-only guard, so it is not a full rollback |
| Workspace deletion with goals and links (two fixtures) | Go author, `ebf9c88e3` | PASS; only this fixture's IDs asserted |
| `pnpm typecheck` | `68bd8a112` (frontend unchanged at `411a122e`) | 9/9 PASS |
| `pnpm lint` | `68bd8a112` | 6/6 PASS, 0 errors (existing warnings only) |
| `pnpm --filter @multica/core test` | `9b0f13cfc` | 238 files, 3158 passed, 1 skipped |
| `pnpm --filter @multica/views test` | `68bd8a112` | 615 files, 7962 passed |
| Desktop tests | `ce63b9cda` | 73 files, 875 passed |
| Desktop build (`bundle-cli` + `electron-vite`) | `ce63b9cda` | PASS |
| Web production build (includes `/[workspaceSlug]/goals` routes) | `68bd8a112` | PASS (exit 0) |
| Web runtime, Linux + Chrome | `9b0f13cfc`, `68bd8a112` | Goal create/view/edit, link/unlink, complete confirmation, reopen, delete, project tab, 404, workspace-member revocation, private-project member revocation, reconnect |
| Desktop runtime, Linux X11 | `ce63b9cda` | Work/project goals, link/unlink, completion, reopen, refresh/back, cross-workspace tabs, revocation and reconnect; final integrated-SHA re-verification pending |
| Independent code review | frontend delta `07a743d5 → 6a1f2835` | PASS (reviewer) |
| Independent code review | Go delta `6a1f2835 → 411a122e` | PASS; supplemental review accepted by the integration manager. All three required code fixes are closed at `411a122e`; this is not final Electron verification or overall quality acceptance. |
| Overall quality-squad acceptance | DEV-5 | Pending |

### Not green / not verified

- **Full-repository `pnpm test` is not green.** `@multica/desktop/src/main/theme-folder.test.ts` has two listener cases that time out under the full parallel turbo run. Evidence: the file is unchanged relative to the delivery start, it passes alone (11/11), and desktop-only tests pass; the failure appears only in the full parallel run, and a views process was also interrupted in the same run. Same-condition before/after attribution is still pending; the final Electron owner is the designated executor. No test was skipped and no timeout was increased.
- **Final Electron verification at `411a122e` is pending.** Existing desktop evidence is at `ce63b9cda` and predates the shared stale-detail fix; in particular `c4-private-goal-after-revoke.png` still shows the old breadcrumb name because of that older SHA. The final run must confirm body, breadcrumb, tab and window title cleanup, plus offline/5xx and reconnect behavior.
- **Overall quality acceptance is pending.** The supplemental Go review passed at `411a122e`, covering owner-pair updates, workspace deletion regression coverage and migration evidence boundaries. The reviewer did not re-run handler tests or migration down/up; those remain the Go author's reported runs. Database-backed tests skipped for missing fixtures do not establish behavior.
- **Whole-DB migration rollback is not supported** by the existing migration 494 forward-only guard; only the goal domain 649–657 was exercised.
- **Known concurrency boundary:** goal creation / project change versus concurrent project deletion is not serialized (no foreign keys, same as existing `issue.project_id`); link operations versus goal deletion/move are serialized.
- **Platform coverage:** Linux Web and Linux X11 Desktop only. Windows and macOS are not verified.
- **Mobile:** no new UI; the existing task API was kept compatible, but no installed-mobile-client test was run.
- **No real agent CLI** was invoked in any test or manual run.
- Web was verified with `next dev` plus a production build, not a deployed production environment.

## Environment and isolation

All database work used isolated development databases on PostgreSQL `15432`; the stable database `multica_xshadow_623`, the stable service at `http://localhost:18703`, its daemon and the shared database container were not stopped, restarted, migrated or overwritten. No database was migrated outside a disposable/isolated test database. No second agent team was initialized.

## Baseline context changes (not part of this increment)

The branch also contains `4fc53397b` `chore(agent): baseline — uncommitted work from the local directory`, which carries pre-existing local workbench context: `.gitignore`, `AGENTS.md`, `Makefile`, `agent-team/CONTEXT.md`, `agent-team/README.md`, `docs/personal-workbench.md`, `scripts/agent-team-context.mjs`, `scripts/agent-team.mjs`, `scripts/agent-team.test.mjs`. These nine files are inherited from the workspace baseline and are not part of DEV-5's product implementation. They were neither restored nor rewritten. The product diff is the 113 files in `4fc53397b..411a122e`; the Go follow-up delta is the 3 files in `6a1f2835..411a122e`.

## Scope reminder

This PR implements goal management and existing-task linking only. It does not implement one-sentence plan generation, dependency-driven execution, automatic goal acceptance, a mobile UI, or changes to the issue/task semantics.

Closes: DEV-5 (Multica work item). This repository has no matching GitHub issue, so no `Closes #<number>` is claimed.
