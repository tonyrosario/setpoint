# Control Planes Sandbox

A practice sandbox: one combined, layered project replicating the essential ideas of Backstage (developer portal), CloudFormation (declarative resource definitions with dependency semantics), and Crossplane (reconciliation-based control plane) — built from scratch as training for platform-engineering and distributed-systems job interviews.

## Language

**Control Plane Core**:
The layer that owns desired state and runs reconciliation loops to converge actual state toward it. The heart of the project; deliberately the deepest layer.
_Avoid_: backend, engine, orchestrator

**Portal**:
The developer-facing surface — a catalog of entities and templates that kick off workflows. Deliberately thin; a presentation and metadata layer over the Control Plane Core.
_Avoid_: frontend, UI, dashboard

**Resource Definition**:
A declarative document (CloudFormation-style) describing desired resources and their dependency ordering. The API into the Control Plane Core — an input format, not a separate engine.
_Avoid_: template (overloaded with Portal scaffolder templates), manifest, stack

**Reconciliation**:
The continuous, level-triggered process of comparing desired state (Spec) to observed state (Status) and acting to close the gap. Reconcilers never depend on having seen every event — they read current state and converge.
_Avoid_: sync, deployment

**Spec**:
The desired-state half of a resource: what the user declared should exist. The durable source of truth for desired state — only user API calls may write it.
_Avoid_: config, definition (that's Resource Definition), desired

**Status**:
The observed-state half of a resource: what the Control Plane Core last saw in the Substrate. Persisted only as a cache; the Substrate is the source of truth, and the reconciler overwrites Status on every pass.
_Avoid_: state, actual, health

**Actor**:
The identity attached to every API write. Recorded as resource owner metadata from day one; authentication/authorization enforcement is deferred.
_Avoid_: user, principal, caller

**Service**:
The flagship Claim kind: one Service expands into a Repository (GitHub Provider, bootstrapped with the delivery-process kit) plus runtime Primitives (Docker Provider). The dogfood golden path.
_Avoid_: app, component, project

**Repository**:
A Primitive managed by the GitHub Provider: a GitHub repo whose Spec declares it exists and carries the bootstrapped delivery process (labels, templates, project board). Drift in these is repaired by reconciliation.
_Avoid_: repo scaffold, workspace

**Composition**:
A platform-authored resource that maps an abstract kind (e.g., WebService) to a template of primitive resources, with patches carrying abstract Spec fields into primitive Specs. The golden-path mechanism.
_Avoid_: blueprint, recipe, stack template

**Claim**:
A developer-submitted instance of an abstract kind defined by a Composition. The developer's unit of interaction — they see Claims, never the primitives underneath. A Claim's Status rolls up from its children's readiness.
_Avoid_: abstract resource, request, instance

**Primitive**:
A resource that maps 1:1 to something a Provider manages in a Substrate (a Docker container, a network). What Compositions expand Claims into.
_Avoid_: managed resource, base resource

**Reference**:
A field in one resource's Spec that points at another resource's Status field (e.g., a container referencing a network's Docker ID). An unresolvable Reference means the resource is not Ready and will be requeued — never an error. References are how ordering emerges without a DAG executor.
_Avoid_: dependency, link, DependsOn

**Ready**:
A resource is Ready when its Substrate object exists and matches Spec, and all its References resolve. Readiness is what dependents wait on.
_Avoid_: healthy, complete, done

**Store**:
The contract through which the Control Plane Core persists resources. In-memory first, SQLite (document-style) as the committed destination.
_Avoid_: database, repository, state store

**Provider**:
A pluggable module that gives the Control Plane Core the ability to observe and mutate one kind of external substrate (Docker, local files, a real cloud). The core talks only to the Provider contract, never to a substrate directly.
_Avoid_: driver, adapter, plugin, integration

**Substrate**:
The external system a Provider manages — something outside the control plane's own process that can genuinely drift and fail.
_Avoid_: target, backend, infrastructure

## Flagged ambiguities

- "Template" is overloaded: Backstage scaffolder templates vs CloudFormation templates. **Resolved**: we use **Resource Definition** for the declarative input document, and the Portal-side scaffolder-template concept does not exist — self-service forms are auto-generated from Composition schemas, and repo generation is a reconciled **Repository** Primitive (ADR-0008), not a template instantiation. Avoid the bare word "template" everywhere.
