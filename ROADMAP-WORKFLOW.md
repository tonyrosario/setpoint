# Roadmap Workflow

This workflow keeps planning, execution, and review visible in GitHub.

## 1. Capture Roadmap Themes

Roadmap themes describe business or product outcomes. Keep them broad enough to group work, but concrete enough to prioritize.

Examples:

- Improve onboarding activation
- Reduce support load from billing confusion
- Make agent-assisted maintenance safe

Recommended GitHub Project fields:

- `Type`: Roadmap Theme
- `Priority`: P0-P3
- `Status`: Inbox or Ready
- `Target`: optional date

## 2. Create Epic Issues

An epic issue turns a theme into a deliverable area of work.

An epic should include:

- Outcome
- Scope
- Non-goals
- Linked features
- Acceptance criteria
- Risks

Use `.github/ISSUE_TEMPLATE/epic.md`.

## 3. Break Epics Into Feature Issues

A feature issue should be independently understandable and reviewable. It may require multiple agent tasks.

A feature should include:

- User-visible behavior
- Technical notes
- Acceptance criteria
- Test expectations
- Rollout notes

Use `.github/ISSUE_TEMPLATE/feature.md`.

## 4. Slice Features Into Agent Tasks

Agent tasks are the only issue type agents should implement.

A good agent task is:

- Small
- Scoped
- Testable
- Low or explicitly accepted risk
- Clear about files, behavior, and constraints
- Labeled `agent-ready` only after human review

Use `.github/ISSUE_TEMPLATE/agent-task.md`.

## 5. Mark Safe Work Agent-Ready

Before applying `agent-ready`, confirm:

- The task has clear acceptance criteria
- Scope boundaries are explicit
- Risky areas are excluded or explicitly allowed
- Dependency changes are either forbidden or explicitly allowed
- Secrets, auth, billing, permissions, infrastructure, and production deploy config are out of scope unless explicitly allowed
- A human would be able to review the resulting PR

### Wave 0: Fix The Stack First

Before fanning work out to parallel agents, run one solo slice that fills the stack layer:

- Verification gate commands (mirror of CI)
- The `.claude/settings.json` toolchain allowlist
- The shared-file list in `AGENTS.md`
- Operational notes in `CLAUDE.md`

Until that slice merges, do not mark stack-touching work `agent-ready`. Tasks that do not touch the stack (docs, template checks) may proceed.

A tracer-bullet feature slice is a good wave-0 vehicle, but any small end-to-end change works. In a docs-only repo the stack layer may legitimately be "no toolchain; verification is a link check."

### Wave Discipline

During grooming and after every merge:

- Apply `blocked` to any `agent-ready` issue with an open "Blocked by" issue.
- Remove `blocked` as blockers merge, releasing the next wave.
- Treat `in-progress` as execution state owned by agents; do not groom it. Remove it only when closing an issue or clearing a stale claim.

## 6. Agent Execution

Agents work from one `agent-ready` task at a time.

Expected flow:

1. Confirm the issue is grabbable: `agent-ready`, not `blocked`, not `in-progress`, all "Blocked by" issues resolved
2. Claim it: add the `in-progress` label, keep `agent-ready` in place
3. Read the issue and linked context
4. Create a branch
5. Make scoped changes
6. Run relevant verification
7. Open a pull request
8. Link the PR to the issue
9. Request human review

The `in-progress` label is the source of truth for claims. Humans remove it when the issue closes or when clearing a stale claim.

Agents should comment on the issue if blocked.

## 7. Human Review

Humans review all PRs before merge.

Review checklist:

- The PR addresses the issue
- Scope did not expand
- Tests or verification are appropriate
- No forbidden areas were touched
- New dependencies were explicitly allowed
- Documentation was updated where needed
- Rollback is understandable

## 8. Merge And Close

Only humans merge. After merge:

- Close the agent task
- Remove `in-progress` from the closed issue
- Remove `blocked` from issues whose last blocker just merged
- Update the linked feature
- Update the project status
- Capture follow-up work as new issues

## Suggested Project Views

Create these manually in GitHub Projects:

- `Roadmap`: group by `Type`, sort by `Priority`
- `Agent Queue`: filter `label:agent-ready -label:blocked -label:in-progress`
- `In Review`: filter by PR-linked issues or `Status: In Review`
- `Blocked`: filter `label:blocked`
- `Done`: filter `Status: Done`

GitHub Project view configuration changes often through the UI, so this bootstrap keeps view setup manual and documented.
