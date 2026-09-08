# Tailmux Worktree Environments — Design

Date: 2026-09-08
Branch: `feat/worktree-environments`
Status: approved design, not yet implemented

## Problem

Per-branch development environments on a remote box currently require
`cal-worktree-kit`: 1,074 lines of Cal-specific shell, Go and Node that give each
git worktree a stable URL, an isolated database, cached dependency installation
and live logs. It works, but it is welded to Cal.com — Yarn, Prisma, Next.js and
Postgres appear throughout — and to one person's machine layout. Nobody else can
use it without forking it.

Roughly two thirds of that kit is not Cal-specific at all: hostname-to-port
routing, per-worktree service supervision, port allocation, lifecycle
reconciliation, a logs viewer, and Tailscale Serve sharing. Tailmux already owns
adjacent machinery — named forwards with per-name HTTP routing, loopback IP
allocation with `/etc/hosts` management, and remote systemd unit generation for
`setup orca`.

This design absorbs the generic two thirds into tailmux and reduces the
Cal-specific third to a config file, so that any project on any stack can get
per-worktree environments.

## Goals

- Any repo declares its own lifecycle; tailmux never learns what a framework is.
- A teammate clones a repo and gets working environments without reinventing config.
- Secrets and machine paths never reach git.
- Multiple hosts per namespace on one client (today's kit allows one).
- The lifecycle is testable in CI with no database and no real project.

## Non-goals

- Tailmux does not provision databases, install dependencies, or understand
  migrations. It guarantees identity, isolation and cleanup; the user supplies
  the verbs.
- Not a general-purpose VPS provisioner. The host arrives with a working
  checkout, runtime and datastore.
- No on-demand auxiliary services (Prisma Studio and friends) in v1.

## Architecture

One binary, two roles.

**Agent (VPS)** — `tailmux agent`, a systemd *user* service with lingering
enabled, no root. Owns port allocation, per-worktree units, the lifecycle
reconciler and the logs endpoint. Sharing via `tailscale serve` is inherently
host-side and would also belong here, but is deferred pending the security
review noted in Open questions.

**Client (Mac)** — the existing tailmux daemon gains the router that maps
`<worktree>.<namespace>.<project>` hostnames to the right forward. This removes
the kit's separate `proxy` binary and `install-mac.sh` entirely; the router
becomes part of the tailmux binary already running.

The client reaches the agent over a forward tailmux already knows how to create.

### Addressing

The kit hardcodes `*.work.cal.localhost` to `127.0.0.1:18080` and
`*.personal.cal.localhost` to `:18081`, which is why it supports only one host
per namespace per client. Tailmux's existing loopback allocation removes that
limit: each host gets its own `127.77.x.x` address with an `/etc/hosts` entry,
so several hosts can serve `*.cal.localhost` names concurrently without
collision.

Pretty URLs still require port 80, and macOS requires root to bind below 1024
regardless of address. Therefore:

- **Default** — privileged router on port 80. One-time `sudo` at install.
  Required in practice for OAuth callbacks and cookie scoping.
- **`--unprivileged`** — high port, URLs carry `:18080`. No root.

### Worktree ownership

`register` is the primitive; `create` is sugar.

- `tailmux worktree register <path>` — adopt a worktree created by anything
  (Orca, a git alias, a human). This is what Orca's setup hook calls.
- `tailmux worktree create <branch>` — perform `git worktree add`, then call the
  same register path.

One code path. Orca stays a first-class citizen rather than a special case.

## Configuration

Two layers. Portable facts travel with the repo; machine facts stay on the host.

### Repo-committed: `tailmux.yaml`

```yaml
version: 1

project: cal                      # namespace component in hostnames
ports:
  app: 3100-3199                  # TAILMUX_PORT allocated from here

hooks:
  setup: |
    cp "$TAILMUX_ROOT_PATH/.env" "$TAILMUX_WORKTREE_PATH/.env"
    yarn install
    node scripts/worktree-db.cjs create "$TAILMUX_DATABASE_NAME"
    yarn prisma migrate deploy

  start: |
    yarn dev --port "$TAILMUX_PORT" --hostname 127.0.0.1

  teardown: |
    node scripts/worktree-db.cjs drop "$TAILMUX_DATABASE_NAME"

health:
  path: /
  timeout: 300s

reconcile:
  archived_marker: ".orca-archived"   # optional; configurable, not hardcoded
```

### Host-local: `~/.config/tailmux/projects/<project>.json`

```json
{
  "root": "/home/sean/work/cal",
  "env": {
    "PATH": "/home/sean/.local/bin:/usr/bin:/bin",
    "DATABASE_URL": "postgresql://...@127.0.0.1:5432/calcom"
  }
}
```

**Precedence: the host file wins, field by field.** A teammate clones the repo,
inherits working hooks, and supplies only `root` and their own secrets.

`env` is the mechanism that keeps secrets out of git: hooks reference
`$DATABASE_URL`, the value lives host-side, tailmux passes it through and never
interprets it.

Port ranges are declared rather than hardcoded, replacing the kit's `3100`,
`18443` and `19443` constants. Tailmux allocates within the declared range and
guarantees no two live worktrees collide.

## Hook contract

All hooks are optional and are plain shell.

| Hook | When | Supervision |
|---|---|---|
| `setup` | once, after the worktree exists | run to completion, serialized per project |
| `start` | to bring the app up | long-running, supervised by tailmux |
| `teardown` | before archive or removal | run to completion |

`start` is supervised — tailmux writes the systemd unit, owns restart-on-failure,
captures output for the logs endpoint, and guarantees the process dies on
teardown. The alternative (hooks daemonise themselves) leaves nothing owning
cleanup and produces orphaned dev servers.

`setup` is **serialized per project** by default. This is not incidental: Cal's
database script needs `pg_advisory_lock` precisely because concurrent worktree
setups race over a shared template. Serializing in the agent means every user
gets that safety without knowing to ask for it. An opt-out exists for projects
whose setup is genuinely independent.

### Environment variables

Passed to every hook. Naming deliberately echoes Orca's `$ORCA_*` variables so
the two systems read as one.

```
TAILMUX_WORKTREE_PATH    /home/sean/orca/workspaces/cal/feat-tags
TAILMUX_ROOT_PATH        /home/sean/work/cal
TAILMUX_WORKSPACE_NAME   feat-tags-assign-tags-to-an-event-type
TAILMUX_ID               5aaa8a200427
TAILMUX_PORT             3111
TAILMUX_HOSTNAME         feat-tags-...-5aaa8a.work.cal.localhost
TAILMUX_NAMESPACE        work
TAILMUX_DATABASE_NAME    calwt_5aaa8a200427
```

### On `TAILMUX_DATABASE_NAME`

This is a **unique, stable, collision-free identifier** derived from the worktree
ID — nothing more. Tailmux never connects to a database, never dumps or restores
one, and does not know whether the project has one at all.

This boundary is deliberate and was validated against Cal's real implementation.
`database.cjs` derives a template name from a hash of host, source database,
user, applied migration checksums and a snapshot epoch; compares
`_prisma_migrations` rows against the migration files present in that specific
worktree; builds the template once inside a `REPEATABLE READ READ ONLY`
transaction using `pg_export_snapshot()`; seals it with
`ALLOW_CONNECTIONS false`; and only then does
`CREATE DATABASE ... TEMPLATE ...`. It also refuses any non-localhost host as a
guard against production.

None of that generalises. Every part is specific to Postgres and Prisma. A
project on MySQL writes different commands; a project with no database ignores
the variable. Documentation carries a Postgres example and a no-database
example, and the boundary stays where it is.

## Lifecycle

```
registered → setting-up → starting → ready
                 ↓            ↓         ↓
               failed      failed    stopped → torn-down
```

- **registered** — path and ID known; port and database name allocated
- **setting-up** — `setup` runs, serialized, output captured
- **starting** — systemd user unit runs `start`, restart-on-failure
- **ready** — `health.path` answered within `health.timeout`
- **torn-down** — `teardown` ran, unit removed, port and name released

The router consumes this state directly: unknown host returns 404; known but not
ready returns a "starting" page linking live logs, rather than a connection
reset. The kit currently surfaces raw `EOF`s in this situation, which tell the
user nothing.

Health checking is what makes readiness honest. A `setup` script exiting zero is
not the same as the app answering requests, so "wait for setup to complete"
alone is not a readiness signal.

### Reconciliation

The agent detects worktrees whose path has vanished, or which carry the
configured `archived_marker`, and runs `teardown` for them. This generalises the
kit's `reconcile.py`, with the marker name configurable so Orca is not
hardcoded.

## Testing

**Unit, no network**
- config parsing and host-file-wins precedence
- port allocation within declared ranges, including exhaustion
- hostname to worktree routing
- hook environment construction
- state machine transitions

**Integration, no real project**
- a fixture repo whose `tailmux.yaml` hooks are `echo` and `sleep`
- full register → setup → start → ready → teardown cycle
- concurrent setup serialization
- cleanup after failure at each stage

The fixture repo is what keeps this maintainable: the lifecycle is exercised in
CI with no Postgres, no Yarn and no Cal checkout.

**Manual smoke** — the real Cal setup on `work/dev-sean`.

## What this replaces

`install-host.py`, `proxy.go`, `reconcile.py` and `install-mac.sh` collapse into
`tailmux agent install` plus the client router. The kit's remaining Cal-specific
logic becomes `tailmux.yaml` plus a database script owned by the Cal repo.

## Resolved

**Adopting the existing dev-sean installation.** A one-off migration reads the
predecessor kit's `routes/*.json` and imports each worktree's host, port, path
and database name into the new state. Deliberately undocumented and unsupported:
there is exactly one such installation and no reason to carry it as a feature.

**Sharing is out of v1.** `tailscale serve` sharing exposes the app, live logs
and a writable Prisma Studio to tailnet peers, and requires narrowly scoped
passwordless `sudo` on the host. It stays in the existing personal tooling until
it has had a security review. v1 delivers the local-URL workflow, which is the
bulk of the value.

**Agent installation uses the documented install script.** The same
`install.sh` that installs the client installs the agent; running it on the VPS
and then `tailmux agent install` is the whole story. No build-from-source on the
host, and no second distribution channel to maintain.

## Open questions

- The install script depends on published releases, which do not exist yet — the
  releases list is empty and both the client and the Cal kit were built from
  source by hand. Cutting a first release is a prerequisite for the agent
  install story above.
- Version-skew policy between a client and an agent on different versions:
  refuse, warn, or negotiate.
