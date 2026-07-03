# Roadmap

Milestone ladder confirmed 2026-07-02. Principle: every milestone ends in a **demoable artifact** and a **tellable interview story** — an unfinished ladder still leaves something whole. M6–M8 are loosely ordered; if a specific interview looms, M8 (real AWS) may jump the queue.

| # | Milestone | Demo | Interview story |
|---|---|---|---|
| M0 | Walking skeleton: in-memory Store, REST API, `Container` Primitive, one reconciler, `cpctl apply/get` | Apply a YAML; a real Docker container appears | Level-triggered reconciliation |
| M1 | Drift repair + References: work queue with backoff; `Network` Primitive; container references `network.status.dockerId` | `docker kill` mid-talk → self-heals; apply resources out of order → ordering emerges | Emergent ordering vs DAG (ADR-0005) |
| M2 | SQLite swap: Store contract proven | Kill the control plane, restart, converge | Spec durability, Status as cache (ADR-0004) |
| M3 | Composition + Claims: `WebService` expands to network + container; Status roll-up | One Claim, whole stack | Golden paths (ADR-0006) |
| M4 | Portal v1: catalog with live readiness; forms auto-generated from Composition schemas | Create a WebService from a form the platform generated for itself | Portal as pure projection |
| M5 | GitHub Provider + `Service` Claim: Repository Primitive bootstrapped with the roadmap kit | One Claim → repo with delivery process + running infra; delete a label → it comes back | Dogfood: the platform manages its own delivery process (ADR-0008) |
| M6 | Graph + change-set views: read-only DAG from References | "Here's what will happen" preview | Plans inform humans, never drive execution (ADR-0005) |
| M7 | Multi-user: OIDC authn + ownership authz, quarrying `aws-s3-self-service` ADRs | Two users, scoped catalogs | Identity threaded from day one made this cheap (ADR-0010) |
| M8 | AWS Provider: hardened S3 bucket kind (modest cost), spec quarried from `aws-s3-self-service` | Same control plane, real cloud resource | The Provider contract was honest (ADR-0001) |
