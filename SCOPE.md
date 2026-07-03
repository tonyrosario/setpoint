# Scope Matrix

The settled feature boundary for the Control Planes Sandbox, as decided in the grilling session of 2026-07-02. Three states: **IN** (committed scope), **DEFERRED** (designed-for; extension points must exist, implementation comes later), **OUT** (deliberately excluded — see the linked ADR before "fixing" this). Terms per [CONTEXT.md](./CONTEXT.md); decisions per [docs/adr/](./docs/adr/).

## Control Plane Core

| Feature | IN | DEFERRED | OUT | Ref |
|---|:---:|:---:|:---:|---|
| Spec/Status split on every resource | ✅ | | | ADR-0002 |
| Level-triggered reconcilers + work queue with backoff | ✅ | | | ADR-0002 |
| Store contract: in-memory first, SQLite document store after | ✅ | | | ADR-0004 |
| Crash-recovery convergence (restart → observe → converge) | ✅ (lands with SQLite) | | | ADR-0004 |
| References resolving through Status; emergent ordering | ✅ | | | ADR-0005 |
| Composition + Claims (simplified golden paths) | ✅ | | | ADR-0006 |
| Change-set-style read-only planning view | | ✅ | | ADR-0005 |
| Optimistic concurrency / multi-writer core | | ✅ | | ADR-0002 |
| Watch protocol, admission webhooks | | | ❌ | ADR-0002 |
| DAG-driven execution, stack state machine, transactional rollback | | | ❌ | ADR-0005 |
| Composition revisions, function pipelines, multi-composition selection | | | ❌ | ADR-0006 |

## Providers / Substrates

| Feature | IN | DEFERRED | OUT | Ref |
|---|:---:|:---:|:---:|---|
| Provider contract (observe/create/update/delete) | ✅ | | | ADR-0001 |
| Docker Provider (first Substrate) | ✅ | | | ADR-0001 |
| GitHub Provider (Repository Primitive, bootstrap-kit drift repair) | ✅ | | | ADR-0008 |
| AWS Provider (hardened S3 bucket first, modest cost) | | ✅ | | ADR-0001, ADR-0009 |
| In-memory chaos/simulation Provider | | ✅ | | ADR-0001 |
| Kubernetes/Kind as Substrate | | | ❌ | ADR-0001 |

## API Surface

| Feature | IN | DEFERRED | OUT | Ref |
|---|:---:|:---:|:---:|---|
| Resource-oriented REST/JSON; declarative writes; 202-async | ✅ | | | ADR-0007 |
| `cpctl` CLI as a plain API client | ✅ | | | ADR-0007 |
| Actor identity on every write; owner metadata on resources | ✅ | | | ADR-0010 |
| AuthN/AuthZ enforcement (OIDC, ownership rules) | | ✅ | | ADR-0010 |
| Imperative action endpoints (`/restart`, etc.) | | | ❌ | ADR-0007 |
| gRPC | | | ❌ | ADR-0007 |

## Portal

| Feature | IN | DEFERRED | OUT | Ref |
|---|:---:|:---:|:---:|---|
| Catalog: Claim list + detail, live readiness, child Primitives | ✅ | | | — |
| Self-service creation: forms auto-generated from Composition schemas | ✅ | | | — |
| Repo generation via Service Claim (dogfoods the roadmap bootstrap kit) | ✅ | | | ADR-0008 |
| Resource graph view (Claims → children → References) | | ✅ | | ADR-0005 |
| Plugin framework | | ✅ | | — |
| TechDocs, external catalog ingestion | | ✅ | | — |
| Scaffolder templates as separately-authored artifacts | | | ❌ (schemas generate forms) | ADR-0008 |
| One-shot imperative scaffolder workflows | | | ❌ (repos are reconciled) | ADR-0008 |

## Project Relationships

| Decision | State | Ref |
|---|---|---|
| One combined, layered project (not three clones) | ✅ | — |
| Monorepo: `core/`, `providers/`, `portal/`, `cli/`, `examples/` | ✅ | ADR-0011 |
| Go core/providers/CLI, TypeScript Portal | ✅ | ADR-0003 |
| `aws-s3-self-service`: quarry + deliberate parallel, no early integration | ✅ | ADR-0009 |
| `github-agent-roadmap-bootstrap`: the GitHub Provider's muscle | ✅ | ADR-0008 |
