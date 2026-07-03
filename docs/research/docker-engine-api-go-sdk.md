# Docker Engine API & Go SDK: Control Plane Integration Brief

> Research brief for the Docker Provider (M0–M1). Produced by a research subagent on 2026-07-03.

## 1. Client Setup

The recommended approach is **`NewClientWithOpts()` with `FromEnv` and `WithAPIVersionNegotiation()`**, which auto-configures from environment variables and performs API version negotiation:

```go
cli, err := client.NewClientWithOpts(
    client.FromEnv,
    client.WithAPIVersionNegotiation(),
)
defer cli.Close()
```

**Current stable version**: `github.com/docker/docker` v28.5.2+ (released Nov 2025). Environment variables (`DOCKER_HOST`, `DOCKER_API_VERSION`, `DOCKER_CERT_PATH`, `DOCKER_TLS_VERIFY`) configure host, API version, TLS certs, and verification. The `WithAPIVersionNegotiation()` option is critical—it prevents errors when the daemon and client disagree on API version by transparently selecting the older version both support.

## 2. Container & Network Lifecycle

**Container operations** are separate steps: create does not start. The SDK provides:
- **`ContainerCreate(ctx, config, hostConfig, netConfig, platform, containerName)`** — returns container ID and warnings. Fails if name conflicts with existing container.
- **`ContainerStart(ctx, containerID, options)`** — starts the created container.
- **`ContainerInspect(ctx, containerID)`** — returns full container state (Config, State, Mounts, NetworkSettings).
- **`ContainerStop(ctx, containerID, options)`** — sends SIGTERM; timeout before SIGKILL.
- **`ContainerRemove(ctx, containerID, options)`** — fails if running and `Force=false`; use `RemoveVolumes=true` to clean volumes.

**Network operations**:
- **`NetworkCreate(ctx, name, types.NetworkCreate)`** — returns network ID; errors on name collision.
- **`NetworkInspect(ctx, networkID, types.NetworkInspectOptions)`** — returns network metadata, IPAM, connected containers.
- **`NetworkRemove(ctx, networkID)`** — fails if containers are still connected.

**Gotchas**: Removing a running container requires explicit `Force=true`. Creating a container with a name that exists (even stopped) errors immediately—the reconciler must handle or pre-check names. Network attachment/detachment happens via `NetworkConnect`/`NetworkDisconnect` after container creation.

## 3. Ownership Labeling

Store ownership metadata using Docker labels—key-value pairs attached at creation:

```go
config := &container.Config{
    Image: "my-image",
    Labels: map[string]string{
        "control-plane.io/owner": "my-provider",
        "control-plane.io/resource-name": "my-container-1",
        "control-plane.io/resource-kind": "Container",
    },
}
```

**Filtering** via `ContainerList()` with label filters:

```go
cli.ContainerList(ctx, types.ContainerListOptions{
    Filters: filters.NewArgs(filters.Arg("label", "control-plane.io/owner=my-provider")),
})
```

This is the reconciler's source-of-truth query: list all containers labeled as owned. Labels persist through inspect and are immutable post-creation; include resource name and kind to correlate observed containers back to resources.

## 4. Observation Strategy: Events vs. Polling

**Events API** (`cli.Events(ctx, types.EventsOptions)`) streams events in real-time as a channel—container lifecycle events (create, start, stop, remove, update, etc.) emit immediately. However, the event stream has two fatal gaps for level-triggered reconcilers:
1. **No durability**: only 256 historical events; older events are lost. If the reconciler crashes or disconnects, it misses events.
2. **No guaranteed delivery**: network glitches, daemon restarts, or backlog scenarios can lose events silently.

**Recommendation: Hybrid approach**—use events as a **wake-up signal** only, never as source of truth:
- Subscribe to container events to detect drift quickly.
- On event receipt, trigger a full `ContainerList()` + `Inspect()` cycle for all labeled resources.
- Periodically poll independently (every 30–60 seconds) regardless of events, to catch out-of-band changes (`docker kill`, manual edits) and recover from event stream gaps.
- Implement reconnection logic with exponential backoff if the event stream closes; stream disconnections are normal.

This ensures level-triggered idempotency: even with a broken event channel, the periodic poll catches all drift.

## 5. Update Semantics

Docker containers **cannot be patched in place**. The `docker update` command can only modify runtime constraints (CPU, memory, restart policy). All structural changes require **delete and recreate**:

| Property | Update In Place? | Method |
|----------|------------------|--------|
| Image | ❌ No | Stop, remove, create with new image, start |
| Environment variables | ❌ No | Recreate |
| Port bindings | ❌ No | Recreate |
| Network attachment | ✓ Partial | Use `NetworkConnect`/`NetworkDisconnect` after creation |
| Volumes | ❌ No | Recreate |

**Provider update() implementation**: compute desired state, compare to observed. For any immutable property change, stop the container, remove it, create the new container with updated config, and start. Volume paths survive container removal if volumes are named or bind-mounted to host paths. Label-based ownership ensures the old and new container instances are correctly associated with the resource.

---

### References

- [github.com/docker/docker/client](https://pkg.go.dev/github.com/docker/docker/client)
- [github.com/docker/docker/api/types/container](https://pkg.go.dev/github.com/docker/docker/api/types/container)
- [github.com/docker/docker/api/types/network](https://pkg.go.dev/github.com/docker/docker/api/types/network)
- [github.com/docker/docker/api/types/events](https://pkg.go.dev/github.com/docker/docker/api/types/events)
- [Docker Engine API docs](https://docs.docker.com/engine/api/)
- [Docker Engine API examples](https://docs.docker.com/engine/api/sdk/examples/)
- [docker system events reference](https://docs.docker.com/reference/cli/docker/system/events/)
- [How to Use Go Docker SDK for Automation (Feb 2026)](https://oneuptime.com/blog/post/2026-02-08-how-to-use-go-docker-sdk-for-automation/view)
- [How to Use Docker Labels for Container Management (Jan 2026)](https://oneuptime.com/blog/post/2026-01-25-docker-labels-container-management/view)
- [How to Use docker system events for Real-Time Monitoring (Feb 2026)](https://oneuptime.com/blog/post/2026-02-08-how-to-use-docker-system-events-for-real-time-monitoring/view)
