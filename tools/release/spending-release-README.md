# Spending Release Helper

Import-only Python helper. Nothing is deployed by running or importing the file.
The parent owns main-API blue/green, drain, backups, UI, source publication and
overall acceptance. This helper never invokes any of those flows.

## Parent Contract

Transfer `spending_release.py` and the UNMODIFIED `longtext_security.py` as
siblings. Transfer these frozen input artifacts outside the new private stage:

- `ztapi-spending-linux-amd64`: Linux x86-64 ELF executable, not a Docker context.
- `spending-columns.json`: JSON map of table names to column-name arrays, exported
  by the frozen audit CLI `--source-columns` from spending.RequiredSourceColumns().

```python
build = {
    "commit": "<40 lowercase hexadecimal characters>",
    "spending_binary": {
        "path": "/absolute/input/ztapi-spending-linux-amd64",
        "sha256": "<64 lowercase hexadecimal characters>",
    },
    "spending_columns": {
        "path": "/absolute/input/spending-columns.json",
        "sha256": "<64 lowercase hexadecimal characters>",
    },
}
baseline["spending"] = {
    "database": "<existing main database name>",
    "db_network": "ztapi_data",
    "db_subnet": "<actual private IPv4 CIDR, e.g. 172.31.0.0/16>",
}
prepared = spending_release.prepare(baseline, build, stage="/absolute/new-stage")
activated = spending_release.activate_route(baseline, stage="/absolute/new-stage")
```

The existing baseline contract is `containers[name].id`,
`containers[name].started_at`, and `nginx_sha256`. Main, Nginx, MySQL, support
and bot must all be present. Extra existing services are pinned too. Never print
the baseline, Docker inspect, private JSON, DSNs or raw external exceptions.
Public entry points return fixed error codes or `operation_failed:<sha256>`.

The extended baseline's optional `spending_networks` map is consumed directly:
keys ztapi_app/ztapi_data/ztapi_egress, each value `{id, internal, driver, ipam}`,
where `ipam` is Docker IPAM.Config. When supplied, every network ID, policy and
nonempty IPAM field is pinned; a same-name/same-subnet recreation is rejected.
App/data must be internal bridges and egress must be an external bridge even
when this optional map is absent. The current baseline-extended.json shape is
compatible without rewriting or printing its contents.

`spending_columns` and its SHA256 are REQUIRED, including in a source checkout.
There is no Go-source parser, local-workspace fallback or implicit source read.
The manifest must have exactly the seven reviewed tables. Each column set must
cover the fixed required minimum and be a subset of the reviewed ceiling,
including WHERE/JOIN/ORDER fields. Settlement `created_at`, `updated_at` and
`charge_dimensions_json` are allowed only when the exported manifest lists them.
A new/unreviewed table/column fails closed, never auto-expands grants. The
runtime uses the same RequiredSourceColumns() for its Preflight, rather than
maintaining a separately parsed helper list. The parent owns frozen audit
CLI/binary/source/commit provenance and the input manifest hash.

`prepare` returns:

```text
prepared=True, service, container, image, commit, build_sha256, nginx_sha256,
spec_sha256, telegram_sha256, default_off=True,
customer_writes=0, telegram_calls=0, model_calls=0
```

`image` is an immutable `sha256:...` Docker image ID, NOT a mutable tag.
`activate_route` returns `activated=True, service, container, nginx_sha256,
reload_only=True` and the same zero-call/write counters.

## Stage And Artifacts

The stage must be a new, absolute directory with an existing parent. Production
requires Linux root, root ownership and mode 0700; symlinked ancestry is refused.
Do not use the old r3 directory, parent output directory, or a directory already
containing input binaries, secrets or receipts. A private imported old-helper
instance receives this STAGE; the parent's helper module/global is not modified.

The ONLY Docker context is `STAGE/spending-build/`, containing `Dockerfile` and
`ztapi-spending` (0555). The recipe uses the inspected, identity-pinned current
main container's immutable image ID, overrides entrypoint/user/working directory
and disables inherited healthchecks. It uses no RUN, COPY --chmod, network,
private configuration or DB dump. No root `.dockerignore` is written.

`STAGE/build.json` hashes ALL build inputs generated in the stage:
`spending-build/Dockerfile`, `spending-build/ztapi-spending`, and
`spending-columns.json`. It also records source commit, base/built image IDs,
safe baked environment defaults, and exact column grants. The manifest itself
is hashed in `prepared.json`.
Activation rechecks those hashes. Private files are excluded from the context
and artifact manifest. Preserve the entire stage for recovery.

Only the new reader's random password is persisted in `reader.private.json`
(root 0600); its ownership proof is separate. Projected `telegram.json` contains
ONLY `bot_token`, is uid/gid 10001 and mode 0400 under the root-0700 stage, and is
bind mounted read-only. The existing bot config must already be a read-only bot
bind at the approved exact host path. The existing support signing key is
copied only into the new service environment. It retains signing capability.

## Safety And Ordering

- Fixed service: `ztapi-spending-20261102-r1`, internal port 3014, no host ports.
  Network membership is exactly ztapi_app + ztapi_data + ztapi_egress. MySQL is
  reached only on ztapi_data; public routing uses app and Telegram uses egress.
  Legacy Docker multi-network limitations are handled by create on app followed
  by explicit data/egress connects, with the created identity saved first.
- Fixed new account: `ztapi_spending_20261102_r1`, host restricted to the inspected
  private DATA bridge subnet/netmask (never app), max two connections. The
  inspected subnet must match baseline.spending.db_subnet. No root/old
  user names, wildcard table grants, DML, DDL privileges or GRANT OPTION.
- Existing unknown accounts fail closed. Owned reruns verify credentials,
  CURRENT_USER, exact grants and connection limit; they never ALTER, reset,
  recreate or regrant an existing account. Partial CREATE is operator recovery,
  not permission to reset. SQL is passed over stdin to the existing MySQL
  container; its root password is never extracted or passed to the sidecar.
- New empty `/opt/ztapi-spending/data` is created as 10001:10001, mode 0700.
  An existing directory requires this stage's device/inode ownership proof.
  Nothing deletes, truncates, restores or takes ownership of an old database.
- Support's DIRECTORY is mounted `/bindings:ro`, including live WAL/SHM, without
  immutable SQLite. The private DB is `/data/spending.db` on a distinct bind.
- Container: uid 10001:10001, read-only root, cap-drop ALL, no new privileges,
  bounded tmpfs, 256 MiB memory+swap ceiling, 0.5 CPU, 64 PIDs, bounded logs.
  Runtime uses SPENDING_QUOTA_PER_UNIT=500000 and SPENDING_READ_DRIVER=mysql.
  Small SQL pools/deadlines are also enforced by runtime and the reader account.
- Baked image environment is allowlisted to PATH/LANG/TZ, GIN_MODE/PORT and
  ZTAPI_SOURCE_COMMIT/RELEASE_VERSION/SOURCE_TAG. Desired Env explicitly includes
  safe defaults, overrides PORT=3014 and this release's source metadata, and
  thereby handles Docker's image-default merge without inheriting DB/provider
  secrets. Unexpected baked keys fail before account creation.
- Preflight needs >=512 MiB available RAM and >=max(256 MiB, 3x binary size)
  available stage/data disk plus Docker memory/CPU/PID limit support. It never
  frees space, kills a process or removes old artifacts to make room.
- `prepare` changes NO Nginx files. It requires healthz JSON readiness, unauth
  GET /api/spending/config=401, and SELECT-only proof that the new preferences
  and alerts tables are empty. It never synthesizes a customer or writes a
  setting to manufacture OFF acceptance. No model, getUpdates or Telegram send.
- `activate_route` repeats health/auth, identity, grants, private projection,
  artifact and empty-store checks BEFORE staging a Nginx proposal. Only the
  public TLS ztapi.vip server's unique /api/support/ anchor is accepted. Unknown
  markers, malformed config or existing spending routes fail closed.
- The route adds ONLY `location ^~ /api/spending/`. Existing API/static/source
  headers and internal catalog listeners remain byte-for-byte unchanged.
  Nginx validates a standalone proposal, compares hashes before atomic rename,
  tests again, reloads (never restarts), and observes new workers. Recovery CAS
  refuses to overwrite a detected external edit. All cooperating edge writers
  MUST share `/run/ztapi-traffic-hold-r6/owner.lock`.
- The parent must ensure the shared edge-lock directory exists securely. Do not
  hold the same flock on another file descriptor while invoking activate_route;
  the lock is deliberately non-reentrant and nonblocking. The helper also takes
  a private stage lock. A lock conflict changes no traffic.
- Both calls require the supplied baseline still to match all pinned services
  and original Nginx bytes. Do both before the parent's main handoff, or refresh
  the baseline and prepare AFTER that handoff. Do not reuse stale Nginx hashes.
  Activation is a pre-traffic operation, not a settings reset or a post-launch
  health command: after real opt-ins exist, use read-only operational checks.

Failures preserve the new service/account/stage and all old services/backups.
Do not advertise fixture tests as a live Docker, MySQL, Nginx or UI verification.

## Offline Verification

```text
python -m unittest discover -s tools/release -p "test*.py" -q
```

Fault tests use fake containers/Docker and temporary SQLite only. The Linux
host adapter additionally has fault coverage for 401 enforcement, Nginx syntax
failure, reload rollback and concurrent-edit preservation. No production or
external network is needed by the tests.
