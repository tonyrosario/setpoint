# setpoint

A from-scratch **internal developer platform (IDP) control plane** in Go: Kubernetes-style level-triggered reconciliation, declarative resources, golden-path compositions, self-healing Docker and GitHub providers, and a TypeScript portal — built to explore how CloudFormation, Crossplane, and Backstage work under the hood.

> **Status: M1 (References + emergent ordering) complete.** The control plane manages two resource kinds — Docker `Container` and `Network` — end-to-end: apply desired state, converge, repair drift, update, and delete, all driven by one level-triggered reconciler. Resources can **reference** each other's observed state, and ordering *emerges* from readiness: apply things in the wrong order and they converge anyway. This is a learning/interview sandbox, not production software; see [Limitations](#limitations).

The name is the control-theory term: a *setpoint* is the desired value a controller continuously drives the actual state toward — a thermostat's target. That is exactly what a control plane is. Here, a resource's **Spec** is the setpoint and the **reconciler** is the controller that closes the gap.

## Quickstart

**Prerequisites:** a running **Docker** daemon, and **Go 1.26** (via [mise](https://mise.jdx.dev): `mise use -g go@1.26`).

Run the full lifecycle in one command:

```bash
./scripts/demo.sh
```

It builds the two binaries, starts the control plane, then walks through: **apply → Ready → drift repair → update-by-recreate → backoff-on-failure → delete**, then the M1 beats — **out-of-order apply → emergent convergence → dependency-kill → cascading self-repair → ordered teardown** — against real Docker containers and networks. It cleans up after itself and is safe to re-run.

### Or drive it by hand

```bash
# Build
( cd cmd && go build -o ../bin/setpointd ./setpointd )
( cd cli && go build -o ../bin/cpctl ./cpctl )

# Start the control plane (REST API on :8080)
./bin/setpointd &

# Apply a Container and watch it converge
./bin/cpctl apply -f examples/container.yaml
./bin/cpctl get containers          # → Ready, with the live container id

# Kill it out-of-band; the reconciler heals it
docker kill setpoint-web
./bin/cpctl get containers          # → back to Ready shortly

# Change the image; it recreates
./bin/cpctl apply -f examples/container-updated.yaml

# References: apply a container that needs a network — BEFORE the network exists
./bin/cpctl delete container web
./bin/cpctl apply -f examples/container-networked.yaml
./bin/cpctl get containers          # → Pending, "waiting for network/backend: no such resource"
./bin/cpctl apply -f examples/network.yaml
./bin/cpctl get containers          # → Ready, attached to the network — no retry needed

# Tear it down
./bin/cpctl delete container web
./bin/cpctl delete network backend
```

`cpctl` is a plain client of the REST API — it has no privileged path into the core. Everything it does, a `curl` could do.

## What just happened — and why it matters

Each step demonstrates a load-bearing idea, the kind an interviewer probes:

- **Declarative, async API.** `apply` sends *desired state* (a Spec) and returns `202` immediately. It never says "do X" — only "this should exist." Convergence happens in the background and is observed via status. This is the declarative lesson, and it forces every client to be honest about eventual consistency.
- **Level-triggered reconciliation.** The reconciler reads *current* state and acts to close the gap toward desired state. It never depends on having seen a particular event. That is why killing a container out-of-band heals: the next reconcile pass simply observes "missing" and creates it. No event replay, no journal.
- **Spec / Status split.** Every resource has a `spec` (desired, written only by you) and a `status` (observed, written only by the reconciler). Status is a cache of what was last seen in the real world; the real world is the source of truth.
- **Ownership by label, not by name.** The Docker provider stamps `setpoint.io/*` labels on what it creates and only ever touches labelled objects — it can find and reconcile *its* resources without a database mapping.
- **Update by convergence.** A changed Spec produces a new spec-hash; the provider sees the running container no longer matches and recreates it (Docker can't patch an image in place). Same machinery as create — no special update path.
- **Honest failure handling.** A bad image doesn't spin the CPU: it fails, records an `Error` status, and retries with per-item exponential backoff. Fixing the Spec converges promptly, because success resets the backoff.
- **Emergent ordering via References — not a DAG.** A container declares `references: {net: {kind: network, name: backend, field: networkId}}` and uses `$(ref:net)` in its Spec; the core resolves the reference through the *target's Status* and gates on the target being Ready. An unresolvable reference means "not Ready yet, check again soon" — **never an error**. This is the ADR-0005 contrast: CloudFormation parses references into a DAG and executes it in topological order, so out-of-order input is rejected up front and a dependency dying *after* creation is invisible to the executor; setpoint has no execution order at all — ordering emerges from readiness, so applying resources backwards just works, and a dependency killed later makes its dependents unready and repairs them. Waiting states are legible: `cpctl get` shows exactly what a resource is waiting for.
- **Replacement cascades through references.** The spec-hash fingerprints the Spec *after* reference substitution, so when a referenced network is recreated (new Docker ID), every container referencing it stops matching and gets recreated onto the replacement — self-repair through the dependency graph with no orchestrator.
- **Graceful shutdown.** `SIGTERM` cancels in-flight reconciles and drains the worker pool — the process exits promptly instead of hanging on a slow call.

## Architecture

A Go monorepo of four modules, with boundaries enforced by module structure (providers and clients import only public contract packages, never core internals):

| Module | Role |
|---|---|
| `core/` | The control plane: resource envelope, `Store` contract (in-memory today), `Provider` contract, the work queue + reconciler, and the REST server. |
| `providers/docker/` | The first Provider — manages Docker containers as an external substrate. |
| `cmd/setpointd/` | The daemon that wires core + providers together (the only place they meet). |
| `cli/cpctl/` | The command-line client — a plain consumer of the REST API. |

The design is deliberately Kubernetes-*inspired* rather than Kubernetes-*based*: it keeps the concepts that carry the lessons (spec/status, level-triggering, a rate-limited work queue) and skips the protocol plumbing (no watch protocol, no resource versions — the periodic resync poll is the safety net).

## Limitations (M1)

Honest about what this does **not** do yet:

- **Single-user, no auth.** The API is unauthenticated and intended to bind loopback for local demos. Multi-user identity is threaded through but not enforced (a deferred milestone), and references can read any resource's observed status — there is no ownership boundary yet.
- **In-memory store.** Desired state does not survive a restart yet; SQLite persistence is the next milestone (M2).
- **Two resource kinds.** Docker `Container` and `Network`, wired together by references. Compositions/Claims (M3), the portal (M4), and additional providers (GitHub, M5+) are on the roadmap.

## Going deeper

- [`CONTEXT.md`](CONTEXT.md) — the domain glossary (Spec, Status, Provider, Reconciliation, Composition, …).
- [`ROADMAP.md`](ROADMAP.md) — the milestone ladder (M0 → M8), each rung a demo and a story.
- [`SCOPE.md`](SCOPE.md) — the IN / DEFERRED / OUT feature matrix.
- [`docs/adr/`](docs/adr/) — architecture decision records: why level-triggered over DAG, why a Provider abstraction, why the spec-hash, and more.
- [`docs/research/`](docs/research/) — background briefs (Docker Engine API, client-go work-queue patterns) that informed the build.

## License

[Apache-2.0](LICENSE).
