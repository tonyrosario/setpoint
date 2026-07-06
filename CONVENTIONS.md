# Conventions

Project conventions for setpoint. Kept short and specific; the domain glossary lives in [CONTEXT.md](CONTEXT.md) and architectural decisions in [docs/adr/](docs/adr/).

## Versioning

**SemVer** (`vMAJOR.MINOR.PATCH`), not CalVer. This is a multi-module Go repo, and Go tooling (`go get`, `GOPROXY`, `go list -m`) parses SemVer tags for module versions — a CalVer tag like `2026.07` is not a valid module version and breaks module resolution.

- **`v0.x` while pre-1.0.** The Store, Provider, and API contracts are still evolving; the `0.` major line signals "expect breaking changes," which is honest for a project mid-roadmap.
- **Milestones map to minor versions.** M0 → `v0.1.0`, M1 → `v0.2.0`, M2 → `v0.3.0`, … (see [ROADMAP.md](ROADMAP.md)).
- **Patch bumps** (`v0.1.1`) are for fixes to an already-tagged milestone, not new milestone work.
- **`v1.0.0`** when the core contracts are stable enough to promise compatibility — likely around the portal / dogfood loop, not before.

**Multi-module note:** the modules (`core`, `providers/docker`, `cmd`, `cli`) currently use local `replace` directives and are not consumed via external `go get`, so a single repo-level tag (`v0.1.0`) is the correct milestone marker. If any module is ever published for independent import, switch that module to Go's per-module tag convention (`core/v0.2.0`, etc.).

## Releases

Each milestone gets an **annotated tag on the milestone-complete merge commit**, promoted to a **GitHub Release**.

```bash
# 1. Annotated tag on main at the milestone merge commit
git tag -a v0.2.0 -m "v0.2.0 — M1: <summary>"
git push origin v0.2.0

# 2. Promote the tag to a Release (attach to the existing tag, don't recreate)
gh release create v0.2.0 --verify-tag --title "v0.2.0 — M1: <title>" --notes "<notes>"
```

Release notes should lead with the **one-command demo**, summarize what's in the milestone, table the PRs that delivered it, and state pre-1.0 limitations and what's next — honest, not marketing.

## Branching & PRs

See [AGENTS.md](AGENTS.md) for the full agent workflow. In short: branch per issue/slice, open a PR linked to the issue, CI must pass, **humans merge** — agents never merge or bypass branch protection.
