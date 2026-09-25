# Project Context: Rootless Podman + Custom Cross-Namespace Bridging

## Goal

Allow rootless Podman containers (each running as a different, separate Linux
user) to be reachable by a single **rootful** Traefik reverse proxy, so one
Traefik instance can front multiple applications that each run under their
own unprivileged user account.

## Background: How pasta fits into rootless Podman networking

- **Podman (not netavark) directly execs `pasta`.** Netavark only configures
  bridges/veth pairs and nftables DNAT rules; it has no code path that
  invokes pasta. Podman is the coordinator that launches both `netavark` and
  `pasta`/`pesto` as external processes.
- **Two distinct pasta usage modes** exist in Podman:
  1. `--network pasta` (direct mode) — pasta is the container's entire
     network namespace provider. Podman launches a dedicated pasta process
     per container.
  2. Rootless **bridge** networks (what `docker compose` creates by
     default) — netavark manages the bridge/veth/DNAT inside the rootless
     netns, and a single **shared** pasta process (one per Linux user
     session, not per container) handles egress/ingress for that whole
     rootless netns. Port publishing for these networks optionally uses
     `rootless_port_forwarder="pasta"` (experimental), which uses a
     companion tool called **pesto** to register/deregister port mappings
     against the shared pasta instance's control socket (`pesto --add` /
     `pesto --delete`), instead of the default `rootlessport` userspace
     proxy.
- **"Kernel-level forwarding" ≠ privileged.** Pasta runs entirely
  unprivileged. "Kernel-level" refers to the _data path_: for local/loopback
  connections pasta uses `splice(2)` / `recvmmsg(2)`/`sendmmsg(2)` to move
  bytes directly between kernel-held sockets (zero-copy, no userspace
  relay), and for external traffic it translates a tap-device L2 interface
  to native L4 host sockets. This is why pasta preserves the original
  source IP, unlike `rootlessport`, which is a true userspace
  accept-and-relay proxy (two separate connections stitched together in
  userspace, hence source IP becomes `127.0.0.1`).
- **Port-forwarding defaults:** Podman always passes `-t none -u none
-T none -U none` to pasta unless a container explicitly requests
  `-p`/`--publish`, which disables pasta's native "scan /proc and
  auto-forward any listening port" behavior. Only explicitly published
  ports get forwarded.
- Pasta options can be set:
  - Globally / process-wide (applies to the shared rootless-netns instance):
    `containers.conf` → `[network] pasta_options = ["-t", "none", ...]`
  - Per-container, **only** for direct `--network pasta` mode (not the
    shared-instance bridge case): `--network pasta:-t,none,-u,none,...`
    (comma-separated pasta args after `pasta:`).
  - There is **no supported way to set different pasta forwarding behavior
    per network** when using the shared rootless-netns instance — it's
    all-or-nothing per user session.

## Custom architecture (in progress)

A custom solution to bridge rootless netns's into a rootful network:

- **Root daemon**: lightweight, listens on a Unix socket, performs
  privileged interface operations (create veth pair, bridge one end to the
  rootful `podman1` interface, move the other end into a target rootless
  network namespace).
- **Client side**: implemented as a **custom netavark driver** used by
  rootless containers; talks to the root daemon to request the
  veth/bridge/move operation when a container connects to a network
  configured with this driver.
- **Usage pattern (docker compose, via Podman socket)**:
  - Default compose network — used for inter-service comms among an app's
    own containers (standard rootless network, e.g. pasta-backed bridge).
  - A second, "external" network (compose `external: true`) — created with
    the custom driver, bridges the rootless containers into the rootful
    network namespace where a single **Traefik** reverse proxy runs,
    proxying traffic for multiple apps, each running as a different
    rootless user.
- **Current limitation**: the custom driver does not yet implement port
  forwarding, to avoid conflicting with pasta's own port
  forwarding/listening setup. Next step is to add port forwarding on the
  bridged interface itself.

## Architecture review notes / open concerns

Raised as things to verify/harden, not blockers:

1. **Root daemon authorization** is the highest-priority risk. The daemon
   must not trust client-supplied PIDs/namespace paths at face value.
   Recommended: use `SO_PEERCRED` on the Unix socket to get the
   kernel-verified UID/PID of the caller, and only allow operations against
   namespaces actually owned by that UID.
   **Fixed 2026-09-17 — see "Code audit findings" § 1 below for details.**
2. **L2/L3 isolation between tenants.** Bridging every rootless user's veth
   onto the same `podman1` bridge as rootful Traefik puts them all in one
   broadcast domain. Without extra isolation (bridge port isolation via
   `bridge link set <if> isolated on`, per-tenant VLAN tagging, or nftables
   rules), tenants' containers can potentially reach/ARP-spoof each other
   directly. Worth deciding whether this matters for the threat model.
   **Fixed 2026-09-17 — see "Code audit findings" § 2 below for details.**
3. **Lifecycle/cleanup robustness.** Confirm the daemon reliably tears down
   veth pairs when a rootless netns disappears (container removed, `compose
down`, user logout, crash) to avoid leaked veth ends/bridge ports.
   Also consider races from concurrent `compose up/down` across users.
   **Functionally fixed as of 2026-09-25 — see "Code audit findings" § 3
   below; unprivileged unit tests landed 2026-09-25, but root-gated
   kernel-side tests and a startup-time orphan sweep remain open, see "Next
   steps".**
4. Noted for context: upstream Podman networking docs already recognize
   "pasta + custom network" as a valid combination, and there's active
   upstream work (PR for `rootless_port_forwarder="pasta"` /
   pesto-based port forwarding) targeting similar source-IP-preservation
   goals for custom networks — worth periodically checking whether upstream
   eventually covers this use case natively.

## Code audit findings (2026-09-15)

Full read-through of every `.go` file in the repo (`server.go`, `client.go`,
`netman.go`, `util.go`, `plugin.go`, `cmd/rootless-netman/main.go`,
`types.go`, `const.go`) against the four concerns above. Status per item:

1. **Root daemon authorization — FIXED (2026-09-17).**

   **Original finding (exploitable as written):**
   - `server.go` (`ServeUnix`): the socket is created `0770` and chowned to
     the _group_ of the socket directory, not root-only — any user in that
     group can dial it.
   - No `SO_PEERCRED` call anywhere in `server.go`. The RPC methods
     (`Connect`, `Disconnect`, `Inspect`) acted on whatever the client sent,
     with no kernel-verified caller identity.
   - The "identity" of the target namespace was entirely client-supplied:
     `types.go`'s `SetupNetworkOptions.ClientPid` / `.ContainerNS` were
     plain ints in the RPC payload; `plugin.go`'s `getContainerNS` had the
     _client_ set `ClientPid = os.Getpid()` and `ContainerNS` = the target
     netns inode, then shipped both to the server; `netman.go`
     (`Connect`/`Disconnect`) and `util.go` (`GetContainerNSPath`) had the
     server `Glob("/proc/<ClientPid>/ns/net")` and match on inode, for both
     connect _and_ disconnect. Since the daemon runs as root, it could
     resolve any PID on the host, not just ones owned by the caller.
   - **Impact:** any user in the socket's group could call
     `Netman.Connect`/`Netman.Disconnect` with a `ClientPid`/`ContainerNS`
     pair pointing at a namespace they don't own (another tenant's
     container, or a guessed/enumerated PID+inode), and get the root daemon
     to bridge a veth into it or tear down another tenant's network. Also
     racy even for legitimate use: `ClientPid` was captured before the
     client called `setns`, giving a TOCTOU window plus ordinary PID-reuse
     risk.

   **Fix implemented:**
   - `util.go` adds `PeerCredentials(conn net.Conn) (*syscall.Ucred, error)`,
     wrapping `SO_PEERCRED` via `SyscallConn`/`GetsockoptUcred` to get the
     kernel-verified UID/PID/GID of a Unix socket peer.
   - `server.go`'s `Serve` no longer hands the listener to a single shared
     `net/rpc` receiver. It accepts connections in a loop and, per
     connection, calls `PeerCredentials` before registering any RPCs;
     unverifiable connections are dropped. Each connection gets its own
     `NetmanReceiver`, bound to that verified peer pid, registered on a
     fresh `rpc.Server`.
   - `netman.go` splits the old single `Netman` interface into a
     client-facing `Netman` (used by `Client`/`Plugin`, unchanged shape) and
     a server-side `Backend` interface whose `Connect`/`Disconnect` take the
     verified pid as an explicit `clientPid int` parameter — the compiler
     now enforces that the pid comes from the authenticated connection, not
     from client-controlled input. `netmanBackend` implements `Backend`.
   - `types.go`'s `SetupNetworkOptions.ClientPid` field is removed
     entirely — there's no client-supplied pid left to trust or ignore.
     `plugin.go`'s `getContainerNS` now only resolves the netns inode.
   - `server.go`'s `Server` holds its `Backend` as a named field
     (`backend Backend`), not embedded, so `Connect`/`Disconnect`/`Inspect`
     are never promoted as unauthenticated public methods reachable outside
     the per-connection RPC path.
   - Covered by `server_test.go` (`TestServerConnectUsesVerifiedPeerPid`,
     `TestServerDisconnectUsesVerifiedPeerPid`, using a `fakeBackend`) and
     `util_test.go` (`TestPeerCredentials`).
   - Socket permissions (`0770`, group-chowned) were deliberately left
     unchanged: once every RPC call is authorized per-connection against the
     kernel-verified peer, broad group access to the socket is no longer
     sufficient to touch another tenant's namespace, so it wasn't part of
     this fix.

2. **L2/L3 tenant isolation — FIXED (2026-09-17).**

   **Original finding:** No bridge-isolation, VLAN, or nftables logic
   anywhere in the codebase; `Connect`/`Disconnect` just forwarded to the
   vendored `go.podman.io/common/libnetwork/network` backend
   (`n.Setup`/`n.Teardown` in `netman.go`), which is external to this repo
   and returns no host-side veth information. No `PerNetworkOptions` or
   driver-specific fields requested port isolation/VLAN tags/nftables
   rules. Every tenant's veth on the shared rootful bridge sat in one flat
   broadcast domain by default. Before the #1 fix, this was worse than a
   passive shared-L2 risk, since an unauthorized party could also actively
   attach/detach into it; after #1, it was back to a passive shared-L2
   exposure between legitimately-connected tenants only.

   **Fix implemented:** rootless tenants can reach the rootful side of the
   bridge (Traefik) but never each other, using Linux bridge port
   isolation — not VLANs or nftables, since `n.Setup` never exposes the
   host-side interface name and the isolated-port flag is a single netlink
   call.
   - `util.go` adds `IsolateContainerInterfaces(nsPath string,
containerInterfaceNames []string) error`: for each named interface
     inside the tenant's netns, it resolves the interface's veth peer via
     `Attrs().ParentIndex` (the kernel-populated `IFLA_LINK`, which points
     at the peer's ifindex in the daemon's own root namespace once `Setup`
     has moved it there), looks that peer up with `netlink.LinkByIndex`,
     and calls `netlink.LinkSetIsolated(hostLink, true)` — an isolated
     bridge port can only forward to non-isolated ports (e.g. Traefik's),
     never to another isolated port.
   - `netman.go`'s `(*netmanBackend).Connect` calls
     `IsolateContainerInterfaces(nspath,
slices.Collect(maps.Keys(statusBlock.Interfaces)))` immediately after
     a successful `n.Setup`. On any isolation failure it calls `n.Teardown`
     to roll back what `Setup` just created and returns the error instead
     of the status block — fail closed, a tenant is never left
     connected-but-unisolated.
   - Unconditional and not configurable: every `Connect` this daemon
     handles is by construction a tenant network on the custom driver, so
     isolation applies every time, with no compose-level opt-out. Rootful
     containers on `podman1` (Traefik) are never marked isolated and
     remain reachable from every tenant, by design — cross-tenant
     connectivity, if ever needed, is explicitly out of scope for this
     daemon and left to a separate solution.
   - Covered by `util_test.go`
     (`TestFindNetInterfaceLinkAndIsolateHostPeer`), which builds a real
     veth pair and bridge and asserts the host-side port ends up isolated.
     Requires root/`CAP_NET_ADMIN` (self-skips otherwise, matching this
     daemon's own privilege requirements) — not yet exercised against real
     privileges in a sandboxed dev environment; run it with `sudo` before
     relying on it in CI.
   - Two review-round bugs caught and fixed before landing: an early draft
     returned a misleading "no veth peer index" error that was immediately
     overwritten by the next statement, and a later draft silently
     discarded `netlink.LinkSetIsolated`'s error (which would have
     defeated the fail-closed guarantee above). Both are fixed in the
     current code.

3. **Lifecycle/cleanup robustness — FIXED (2026-09-17, revised through 2026-09-25).**

   **Original finding:** `Disconnect`/`Teardown` (`netman.go`, `plugin.go`)
   only ran when the netavark plugin protocol explicitly invoked `teardown`;
   there was no daemon-side watcher (inotify on `/run/netns`, netlink
   namespace-removal notification, or periodic reconciliation) that detected
   a rootless netns disappearing without a clean teardown call (crash,
   `kill -9`, OOM, abrupt logout), and no startup-time orphan sweep in
   `main.go`/`server.go`. The `net/rpc` server also serves requests
   concurrently by default with no visible locking around the shared
   `storage.GetStore` state used by all tenants.

   Investigation confirmed the leak is real: the container/tenant netns this
   daemon bridges into is a persistent, bind-mounted namespace file (e.g.
   `/run/netns/<name>`), not a `/proc/<pid>/ns/net` reference tied to a live
   process. Kernel veth-pair semantics mean a *fully completed* teardown
   never strands a host-side interface (destroying the namespace
   cascade-deletes both veth ends), but if nothing ever successfully calls
   this daemon's `Disconnect` — e.g. conmon itself is killed before running
   its exit-cmd, or (more specific to this daemon) the plugin's RPC to this
   privileged root daemon fails because the daemon happened to be down at
   that moment — the bind-mounted netns file plus its veth/bridge port can
   persist indefinitely with nothing left to trigger cleanup. Podman's own
   default networking has the identical structural problem and solves it
   with file-based refcounting plus lazy self-healing rather than a
   background watcher (`go.podman.io/common`'s
   `libnetwork/internal/rootlessnetns`), but that doesn't fully cover this
   daemon: a failed teardown call there is silently absorbed once the
   container record is removed, and none of it has any visibility into this
   daemon's own veth/bridge/isolation state regardless.

   **Current design (as implemented):**
   - `state.go` defines a `StateStore` interface (`Put`, `Get`, `Delete`,
     `GetLinks`) backed by `stateStore`, a `map[string]ContainerLinks` keyed
     by `ContainerID + "/" + Network.Name`, persisted to
     `/run/rootless-netman/state.json` via atomic temp-file-then-rename on
     every mutation (`NetmanStateDir`/`NetmanStatePath`, `const.go`).
     `loadStateStore` tolerates a missing file (starts empty) and a corrupt
     one (logs and starts empty) without failing daemon startup.
   - `links.go` defines `ContainerLinks` (`Options`, `NSPath`, `NSInode`,
     `Interfaces`), the record `Connect` stores for later reconciliation, with
     `Isolate()` (bridge-port isolation, moved here from `util.go`'s old
     `IsolateContainerInterfaces`) and `Exists()` (namespace-inode check via
     `GetNsInode`, then per-interface existence via `netns.GetFromPath` +
     `netlink.NewHandleAt` + `LinkByName`).
   - `netmanBackend` (`netman.go`) holds `containerNetwork
     types.ContainerNetwork`, `state StateStore`, `cleanupInterval
     time.Duration`, and a `mu sync.Mutex` serializing every
     `Setup`/`Teardown` call plus the state mutation around it, across
     `Connect`, `Disconnect`, and `cleanup` — the in-process analogue of
     netavark's own `lockfile.LockFile` (which only exists because podman has
     no single resident process to hold an in-memory lock in).
     `cleanupInterval` is set via the `Option`/`CleanupInterval(interval)
     Option` functional-options pattern, which rejects `interval <= 0` so a
     bad `-cleanup-interval` value fails `NewNetman`/`NewServer` cleanly
     instead of later panicking `time.NewTicker` with a non-positive
     duration.
   - `NewNetman` loads the state store, applies options, and starts
     `go backend.cleanupLoop()` — a `time.Ticker`-driven goroutine (no stop
     channel, matching the daemon's long-running systemd lifecycle) that
     walks `state.GetLinks()` every `cleanupInterval` (`DefaultCleanupInterval`
     = 60s, `const.go`) and calls `cleanup(conn)` for anything `!conn.Exists()`.
     `cleanup` always attempts `Teardown` even when the namespace looks fully
     gone — netavark's own IP allocations live in an IPAM db keyed
     independent of namespace existence (`deallocIPs`, `go.podman.io/common`'s
     `libnetwork/netavark/run.go`), so skipping the call would leak that
     reservation — and drops the record unconditionally afterward regardless
     of the `Teardown` result. There is currently no startup-time sweep
     before `cleanupLoop`'s first tick; a restart's orphans wait up to one
     `cleanupInterval` before being reconciled.
   - **NSPath is a pinned bind mount, not a `/proc/<pid>/ns/net` reference —
     this was a real bug caught during implementation, not just a hardening
     choice.** `Connect`'s RPC caller is a one-shot `rootless-netman
     setup`/`teardown` CLI invocation (`plugin.go`) that exits almost
     immediately after the call returns; the daemon resolves it via
     `GetContainerNSPath(clientPid, options.ContainerNS)` (`util.go`), which
     returns the literal string `/proc/<clientPid>/ns/net`. That's safe to
     use for the *synchronous* Setup/Teardown call (the plugin process is
     guaranteed alive and `setns()`'d into the namespace for the RPC's
     duration), but an earlier revision persisted that same ephemeral string
     into `ContainerLinks.NSPath` for the *reconciler* to reuse much later —
     once the plugin pid was reaped and recycled by the kernel to an
     unrelated process, `cleanup`'s `Teardown` call would have operated
     against whatever namespace that recycled pid currently belonged to. Now
     fixed: `Connect` immediately pins the verified `/proc/<pid>/ns/net` via
     `podmannetns.NewNSFrom` (`go.podman.io/common/pkg/netns`, the same
     bind-mount-pinning helper podman's own `rootlessnetns` package uses
     internally) and uses that durable path — never the ephemeral one — for
     `Setup`, `Isolate`, the stored `NSInode`, and `ContainerLinks.NSPath`.
     A `tracked` flag plus a deferred `podmannetns.UnmountNS` releases the
     pin on any failure path (`Setup` failure, isolation failure, inode-read
     failure) so nothing leaks when a connection never makes it into state.
   - **Fixing NSPath also required closing a cross-tenant authorization gap
     in `Disconnect`.** Once `Disconnect` could use a tracked connection's
     pinned path instead of re-deriving one, matching purely on the
     client-supplied `ContainerID`/`Network.Name` strings would have let any
     authenticated caller name another tenant's `ContainerID`/`Network` and
     get the daemon to tear down and unmount its pin — reopening the class of
     bug the original `SO_PEERCRED` fix (§1) closed. `Disconnect` therefore
     still verifies the caller's current namespace via `GetContainerNSPath`
     first, and only trusts/uses a tracked record's pinned `NSPath` when that
     record's own `NSInode` matches the inode the caller just proved
     ownership of; otherwise it falls back to the verified path and leaves
     the mismatched record untouched.
   - **Implementation history worth knowing:** the concurrency model went
     through a channel-based single-goroutine actor rewrite (request/response
     structs, a `serverLoop` `select`) and was reverted back to the
     `sync.Mutex` design above after review found it introduced a guaranteed
     deadlock (a missing channel send), a guaranteed startup panic (a dropped
     `cleanupInterval` reaching `time.NewTicker(0)`), and coupled the
     periodic cleanup sweep to the same dispatch loop as live RPCs (so a slow
     orphan `Teardown` would stall every tenant's `Connect`/`Disconnect`/
     `Inspect`). The mutex design doesn't have that coupling problem and had
     no live correctness bugs found against it across three review rounds,
     so it's the one to keep building on.
   - **Test coverage: backend logic covered (2026-09-25), kernel side still
     open.** Every kernel-facing call in `netmanBackend` now goes through the
     `nsOps` interface (`nsops.go`: `ResolvePath`, `Pin`, `Unpin`, `Inode`,
     `Isolate`, `Exists`; production impl `kernelNSOps`), so
     `netman_test.go` can fake it alongside a fake
     `types.ContainerNetwork` and run unprivileged. It covers `Connect`
     (pinned-path tracking, unverified-namespace rejection, pin failure,
     rollback/unpin on failure), `Disconnect` (tracked pinned path,
     ignoring another tenant's record via the inode-ownership check,
     untracked fallback, unverified-namespace rejection, record kept on
     `Teardown` failure), `cleanup`/reconciliation (drops the record even if
     `Teardown` fails, only touches missing connections), `Inspect`, and
     `CleanupInterval` rejecting non-positive values. `state_test.go` covers
     `stateStore`'s put/delete/reload and corrupt-/missing-file recovery.
     **Still open:** `util_test.go`'s
     `TestFindNetInterfaceLinkAndIsolateHostPeer` — the only test that ever
     exercised bridge-port isolation against a real veth/bridge — was
     deleted when that logic moved into `ContainerLinks.Isolate()`/`Exists()`
     in `links.go`, and nothing replaces it yet. `kernelNSOps`,
     `Isolate()`/`Exists()`, and real pin/unmount need root-gated tests
     (root/`CAP_NET_ADMIN`/`CAP_SYS_ADMIN`, self-skipping otherwise) before
     relying on this path in CI.

4. Upstream pasta/pesto tracking — not applicable to a code review.

**Bottom line:** #1 was a live, concretely exploitable authorization bypass
(not just a hardening gap) — fixed 2026-09-17 via `SO_PEERCRED`-based
per-connection authentication. #2 (shared L2 with no isolation between
tenants) was the next-highest-priority item once #1 closed the
unauthorized-attach path, and is also fixed 2026-09-17 via bridge port
isolation. #3 (lifecycle/cleanup robustness) is functionally fixed as of
2026-09-25 via daemon-side connection tracking, a periodic cleanup loop,
`sync.Mutex` serialization, and (the sharpest of several bugs caught along
the way) pinning `NSPath` to a durable bind mount instead of an ephemeral
`/proc/<pid>/ns/net` reference. Its backend logic is covered by unprivileged
unit tests against a faked `nsOps` (2026-09-25); what's *not* yet closed is
root-gated testing of the kernel side (`kernelNSOps`, `Isolate()`/`Exists()`,
real pin/unmount) — see the last bullet above — and there's no startup-time
orphan sweep yet either.

## Next steps

- [ ] Implement port forwarding in the custom netavark driver, operating on
      the bridged rootful/rootless interface (not via pasta), since pasta's
      default `-t none -u none -T none -U none` already means it won't
      interfere as long as the default-network compose services don't
      declare `ports:`.
- [ ] Decide whether default-network compose services should ever use
      `-p`/`ports:` at all, or whether all external exposure should route
      exclusively through the custom bridged network + Traefik (simplest,
      avoids any pasta/custom-driver conflict).
- [x] Add UID/PID verification (`SO_PEERCRED`) to the root daemon's request
      handling — confirmed missing by code audit (2026-09-15); fixed
      2026-09-17; see "Code audit findings" above.
- [x] Decide on and implement a tenant isolation strategy on the shared
      bridge (port isolation, VLANs, or nftables) if cross-tenant traffic is
      undesired — implemented via Linux bridge port isolation; confirmed
      missing by code audit (2026-09-15); fixed 2026-09-17; see "Code audit
      findings" above.
- [x] Audit daemon cleanup path for veth/bridge-port leaks on container/netns
      teardown — confirmed missing by code audit (2026-09-15); functionally
      fixed as of 2026-09-25 via daemon-side state tracking (`state.go`,
      `links.go`), a periodic `cleanupLoop` (`netman.go`), `sync.Mutex`
      serialization, and pinning `NSPath` to a durable bind mount instead of
      an ephemeral `/proc/<pid>/ns/net` reference; see "Code audit findings"
      §3 above for the full history, including a channel-based rewrite that
      was tried and reverted. The initial implementation plan (superseded by
      the NSPath-pinning revision) is at
      `~/.claude/plans/curious-knitting-moth.md`.
- [x] Unprivileged unit tests for `Connect`/`Disconnect`/`cleanup`/
      `reconcileOnce`, the pin/unpin lifecycle, and the `Disconnect`
      inode-ownership check (2026-09-25): every kernel-facing call in
      `netmanBackend` goes through the `nsOps` interface (`nsops.go`,
      production impl `kernelNSOps`), faked in `netman_test.go` alongside a
      fake `types.ContainerNetwork`. Runs as a normal user so VS Code's test
      runner and coverage work.
- [ ] Add root-gated tests for the kernel side only — `kernelNSOps` /
      `ContainerLinks.Isolate()`/`Exists()` and pin/unmount against a real
      namespace/veth pair/bridge (replacing the deleted
      `TestFindNetInterfaceLinkAndIsolateHostPeer`). Run with
      `go test -exec sudo -coverprofile=cover.out ./...` and load the profile
      in VS Code via "Go: Apply Cover Profile".
- [ ] Add a startup-time orphan sweep before `cleanupLoop`'s first tick, so a
      daemon restart doesn't leave crash orphans unreconciled for up to one
      `cleanupInterval`.
