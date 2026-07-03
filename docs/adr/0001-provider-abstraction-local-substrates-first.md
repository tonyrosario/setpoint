# Provider abstraction, local substrates first, real cloud later

The Control Plane Core needs external substrates to reconcile against. Real cloud (AWS) is realistic but burns sandbox time on IAM/billing; pure in-memory simulation makes reconciliation circular (nothing drifts unless we fake it). We decided the core talks only to a **Provider** contract (observe/create/update/delete on externally-owned resources), with **Docker as the first Provider** — free, laptop-demoable, and genuinely drifts (kill a container, watch the reconciler repair it). A real AWS Provider is a planned later addition at modest cost, and must land as a new module with zero changes to the core; that constraint is the test that the abstraction is honest.

## Considered Options

- Real cloud first — rejected: credential/billing yak-shaving displaces control-plane learning.
- In-memory fake cloud only — rejected: reconciling state we also own proves nothing; kept as a possible chaos-testing Provider later.
- Kubernetes (Kind) as substrate — rejected: a control plane on a control plane muddies the "from scratch" story.
