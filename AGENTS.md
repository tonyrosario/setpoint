# Agent Instructions

This repository uses GitHub as the source of truth for roadmap and delivery work.

## Before Starting

Confirm the issue is labeled `agent-ready`. If it is not, do not implement it.

Read:

- The assigned issue
- Linked parent issues
- Relevant repository docs
- Existing code near the change

## Rules

You may:

- Create a branch
- Make scoped changes
- Commit changes
- Open a pull request
- Request human review
- Comment with blockers

You may not:

- Merge pull requests
- Enable auto-merge
- Push directly to protected branches or bypass branch protection
- Change roadmap priorities
- Work on issues not marked `agent-ready`
- Introduce dependencies unless explicitly allowed by the issue
- Touch secrets, auth, billing, production deploy config, permissions, or infrastructure unless explicitly allowed by the issue
- Expand scope beyond the issue

## Workflow

1. Restate the task in your own working notes.
2. Inspect the repository before editing.
3. Make the smallest useful change.
4. Run relevant verification.
5. Open a PR linked to the issue.
6. Include verification and limitations in the PR body.

## If Blocked

Stop and comment on the issue with:

- Why you are blocked
- What you tried
- What you need from a human

## Pull Requests

Every PR must include:

- Linked issue
- Summary
- Verification
- Known limitations
- Confirmation that forbidden areas were not touched unless explicitly allowed

Humans merge. Agents do not merge.

Branch protection or repository rulesets must remain the human review gate. If protections appear missing or bypassable by agent credentials, stop and report a blocker instead of proceeding to merge.

## Conventions

See [CONVENTIONS.md](CONVENTIONS.md) for versioning (SemVer; milestones → minor versions) and release (annotated tag + GitHub Release per milestone) conventions before tagging or cutting a release.
