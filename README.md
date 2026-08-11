# OpsRamp Agent Orchestrator

A control plane for monitoring agents, covering **two agent planes at once**:

- **OpAMP-native agents** connect to the built-in [OpAMP](https://github.com/open-telemetry/opamp-go)
  server over WebSocket. The orchestrator persists their state and reconciles
  versioned **remote config** and **packages** back to them.
- **OpsRamp agents** do not speak OpAMP — they are managed through the OpsRamp
  **REST/OAuth v2 API**. The orchestrator authenticates to a tenant, polls the
  agent inventory, proxies the agent-management API, and can **install, repair,
  upgrade, or remove the OpsRamp agent across a fleet of VMs over agentless SSH**.

Both planes share one store, one operator REST API, and one dashboard.

```
                       ┌──────────────────── orchestrator ─────────────────────┐
                       │                                                       │
  OpAMP agents ───ws──▶│  OpAMP server ──┐                                     │
  :4320                │                 │                                     │
                       │                 ├── store ──┬── admin API + UI ◀──────┼── operators
  OpsRamp tenant ◀─────┤  OpsRamp        │           │   :8080                 │   :8080
  (REST/OAuth v2)      │  connector ─────┘           │                         │
                       │       │                     ├── reconcile engine      │
                       │       └── inventory poller  │   (drift → recommend)   │
                       │                             │                         │
  VM fleet ◀────ssh────┤  deploy manager ────────────┘                         │
  (install/repair/…)   │                                                       │
                       └───────────────────────┬───────────────────────────────┘
                                            Postgres
```

---

## Contents

- [Capabilities](#capabilities)
- [Quick start](#quick-start)
- [Dashboard](#dashboard)
- [Configuration](#configuration)
- [OpsRamp connector](#opsramp-connector)
- [Fleet operations over SSH](#fleet-operations-over-ssh)
- [OpAMP config reconciliation](#opamp-config-reconciliation)
- [Admin API reference](#admin-api-reference)
- [Data model](#data-model)
- [Security model](#security-model)
- [Troubleshooting](#troubleshooting)
- [Development](#development)
- [Project layout](#project-layout)

---

## Capabilities

| Area | What it does |
|------|--------------|
| **Agent registry** | Persistent identity, attributes, labels, capabilities, first/last seen, connection status. |
| **Health & status** | Tracks reported `ComponentHealth`, effective config, remote-config apply status, package status. |
| **Remote config** | Versioned, immutable config maps per **group**; hash-based reconciliation; instant push to connected agents; one-click **rollback**. |
| **Grouping** | Agents map to a group by explicit assignment or a **label selector**; otherwise the default group. |
| **Packages** | Upload agent binaries/addons, assign to groups, offer via OpAMP `PackagesAvailable`; agents download and report `PackageStatuses`. |
| **OpsRamp connector** | OAuth2 client-credentials auth with automatic token refresh, inventory polling, and a proxy for the `/v2/api/agents` surface. Configured from the UI, stored in the database. |
| **Fleet operations** | Install / preflight / repair / upgrade / uninstall the OpsRamp agent across a VM fleet over **agentless SSH** — IP, CIDR and range targets, jump-host support, per-host results. |
| **Reconciliation** | Continuous drift detection (agents down, versions behind the fleet's newest) producing approval-gated remediation recommendations. |
| **Observability** | Event log of every connect / disconnect / config-apply / failure; `/healthz` and `/readyz`. |
| **Security** | Optional bearer tokens for agents (`OPAMP_AUTH_TOKEN`) and for mutating admin calls (`ADMIN_AUTH_TOKEN`). SSH credentials live in memory for the duration of a job and are never persisted; host keys are pinned on first use. |

---

## Quick start

Requirements: Docker with Compose v2. (For local builds: Go 1.25+.)

```bash
git clone https://github.com/cyberrule92/opsramp-agent-orchestrator.git
cd opsramp-agent-orchestrator

make up                              # postgres + orchestrator + 1 demo agent
open http://localhost:4777           # dashboard
```

Compose publishes the container's ports on different host ports:

| Service | Container | Host | Purpose |
|---------|-----------|------|---------|
| Admin API + dashboard | `8080` | **`4777`** | operators, this README's `BASE` |
| OpAMP endpoint | `4320` | **`24320`** | agents outside the compose network |

Seed an example config into the `default` group, scale the demo fleet, tear down:

```bash
make seed BASE_URL=http://localhost:4777
make scale-agents N=5
make down                            # keep data
make clean                           # remove volumes too
```

Running the binaries directly (no compose) puts the dashboard on `:8080` and
OpAMP on `:4320` — see [Development](#development).

---

## Dashboard

Four views, all served from a single embedded page (no build step, no CDN):

| View | What it shows |
|------|---------------|
| **Overview** | Fleet counters, recent deploy jobs, connector state at a glance. |
| **OpsRamp Inventory** | Agents discovered from the OpsRamp Resources API — name, host, IP, OS, agent version, status. Select rows to launch a bulk operation. Holds the **Connector** settings panel. |
| **Reconcile** | Drift report: agents down, agents behind the newest version in the fleet, and the remediation each one needs. |
| **Fleet Operations** | Launch and monitor install / preflight / repair / upgrade / uninstall jobs, with per-host output. |

The page carries an `ETag` and `Cache-Control: no-cache`, so a redeploy is picked
up on the next load rather than being served from a stale browser cache.

---

## Configuration

All configuration is environment-based, except the OpsRamp connector, which is
stored in the database and editable from the UI.

| Var | Default | Notes |
|-----|---------|-------|
| `DATABASE_URL` | *(required)* | pgx/libpq DSN |
| `OPAMP_LISTEN_ENDPOINT` | `:4320` | agent-facing listener |
| `OPAMP_LISTEN_PATH` | `/v1/opamp` | OpAMP path |
| `ADMIN_LISTEN_ENDPOINT` | `:8080` | admin API + UI |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | base for package download URLs handed to agents — must be reachable *by the agents* |
| `DEFAULT_GROUP` | `default` | fallback group |
| `OPAMP_AUTH_TOKEN` | *(empty)* | if set, agents must send `Authorization: Bearer …` |
| `ADMIN_AUTH_TOKEN` | *(empty)* | if set, mutating admin calls need the bearer token |
| `DEPLOY_STATE_DIR` | `/var/lib/orchestrator` | holds the TOFU `known_hosts` store (compose sets `/home/nonroot`) |
| `DEPLOY_CONCURRENCY` | `10` | max hosts operated on in parallel per job |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `OPSRAMP_API_URL`, `OPSRAMP_TENANT_ID`, `OPSRAMP_CLIENT_KEY`, `OPSRAMP_CLIENT_SECRET`, `OPSRAMP_POLL_INTERVAL` | *(empty)* | **seed only** — see below |

> **`DEPLOY_STATE_DIR` is not on a volume in the default compose file.** Pinned
> SSH host keys are lost when the container is recreated, and the next
> connection re-pins on first use. Mount a volume there if you want the pins to
> survive redeploys.

---

## OpsRamp connector

OpsRamp agents are managed through the OpsRamp REST/OAuth v2 API. The connector
authenticates, polls the agent inventory (so OpsRamp agents appear in the
dashboard), and proxies the [`/v2/api/agents`](https://develop.opsramp.com/v2/api/agents)
management surface.

### Set it up from the dashboard

No credentials need to live in backend config. Open **Inventory → Connector →
Settings** and enter the API URL, tenant id, and the client key/secret from an
OpsRamp Integration:

- **Test connection** — authenticates and probes the tenant *without saving
  anything* or disturbing the live client. It reports the two failure modes
  separately, because they have different fixes: a bad key/secret (`401 Bad API
  credentials`) versus valid credentials paired with a tenant id the token
  cannot reach (`410 No active organization found with id …`).
- **Get token** / **Force refresh** — show the live Bearer token (hidden by
  default, with copy and a ready-made `curl` line) and when it expires, so an
  operator can reproduce any API call by hand.
- **Save & connect** — validates, persists to the `opsramp_config` table, and
  hot-swaps the live client and poller. No restart.

The environment variables below are optional: they only *seed* the initial
config on first boot when the database has none. Stored settings win from then
on.

```bash
OPSRAMP_API_URL=https://pod7.api.opsramp.com    # your pod's API host
OPSRAMP_TENANT_ID=client_xxxxxxxx               # tenant / client id
OPSRAMP_CLIENT_KEY=...                          # integration key
OPSRAMP_CLIENT_SECRET=...                       # integration secret
OPSRAMP_POLL_INTERVAL=60s
```

### Auth flow and token handling

`POST {API_URL}/tenancy/auth/oauth/token` with `grant_type=client_credentials`
(form-encoded) returns a Bearer token with scope `global:manage`. API calls then
go to `{API_URL}/api/v2/tenants/{tenantId}/…`.

Two OpsRamp behaviors shape the implementation, and both are worth knowing when
debugging:

1. **The token is tenant-wide and shared.** Repeated token requests return the
   *same* `access_token` with `expires_in` counting down its **remaining** life
   (full TTL ≈ 7199s), not a fresh 2h token each time.
2. **A rejected token comes back as HTTP 407** with an `InvalidTokenException` /
   `invalid_token` body — not the usual 401. It reads like a proxy error.

So the client refreshes a minute before expiry, never trusts a token for longer
than its 2h maximum regardless of what the endpoint reports, and treats both
`401` and `407` as auth failures. Every agent operation — a deploy job, an
inventory sync, an admin proxy call — checks the token first and renews it if
expired; any call rejected as unauthorized re-authenticates and retries once.
`GET /api/v1/opsramp/status` reports `token_expires_at` for the cached token.

### Connector endpoints

```
GET  /api/v1/opsramp/config                           current settings (secret masked)
PUT  /api/v1/opsramp/config                           save settings and reconnect
POST /api/v1/opsramp/test                             validate credentials without saving them
POST /api/v1/opsramp/token                            current Bearer token ({"refresh":true} forces a new one)
GET  /api/v1/opsramp/status                           configured? authenticated? inventory count
GET  /api/v1/opsramp/agents                           synced agent inventory (from Resources Search)
POST /api/v1/opsramp/sync                             sync inventory now
GET  /api/v1/opsramp/agents/{platform}/info           proxy: agent package details
GET  /api/v1/opsramp/agents/{platform}/download/{pkg} proxy: download agent package (stream)
POST /api/v1/opsramp/updates                          proxy: configure agent auto-updates
POST /api/v1/opsramp/policies/{policyId}/devices      proxy: assign resources to an agent policy
POST /api/v1/opsramp/profiles/{profileId}/devices     proxy: assign resources to a master profile
```

`/test` and `/token` hand back a live Bearer token, and both are `POST` so that
`ADMIN_AUTH_TOKEN` gates them. **Set that token on any deployment whose admin
port is reachable by anyone you would not hand tenant credentials to.**

```bash
BASE=http://localhost:4777

curl -sX POST $BASE/api/v1/opsramp/test  -H 'content-type: application/json' -d '{}'
curl -sX POST $BASE/api/v1/opsramp/token -H 'content-type: application/json' -d '{"refresh":true}'
```

Inventory comes from the Resources Search API (`agentInstalled:true`), mapped to
`agentVersion` / `agentStatus` / host / IP and stored in `opsramp_agents`. When
credentials are absent the connector is disabled and these endpoints return
`503` with a clear message — the rest of the orchestrator runs normally.

Implementation: the `/v2/api/agents` mapping is in `internal/opsramp/api.go`,
OAuth and transport in `internal/opsramp/client.go`, live reconfiguration in
`internal/opsramp/connector.go`, and the inventory poller in
`internal/opsramp/poller.go`.

---

## Fleet operations over SSH

Beyond *monitoring* OpsRamp agents, the orchestrator can **act on the fleet** —
installing and managing the OpsRamp agent across many VMs at once over agentless
SSH (a self-contained Go fan-out; no Ansible or external tooling). Everything is
one pipeline, distinguished by an **action**:

| Action | What it does | Needs connector? |
|--------|--------------|------------------|
| `preflight` | Read-only readiness probe per host: OS, kernel, arch, `sudo`, existing agent, root disk free, OpsRamp API reachability. Changes nothing. | no |
| `install` | Fetches OpsRamp `deployAgent.sh` (`scriptType=SHELL`) and runs it over SSH. | **yes** |
| `repair` | Re-runs the installer on hosts whose agent is **down**, restoring it. | **yes** |
| `upgrade` | Re-runs the installer on hosts **behind the newest fleet version**. | **yes** |
| `uninstall` | Removes the agent (`dpkg -P` / `rpm -e` → `rm -rf /opt/opsramp/agent`); optionally **deregisters** the resource from OpsRamp. | only if deregistering |

A background **reconcile engine** continuously evaluates the inventory for down
and version-drifted agents and surfaces remediation **recommendations**. They are
approval-gated: applying one opens a targeted operation where you supply SSH
credentials, which are never stored.

### Setup

**1 — Bring up the stack** and confirm the connector is authenticated:

```bash
make up
curl -s http://localhost:4777/api/v1/opsramp/status   # {"configured":true,"authenticated":true,…}
```

**2 — Get the agent install keys.** The installer's `-K` / `-S` are the **agent
access and security keys of an OpsRamp *Installed Integration*** — these are
**not** the REST OAuth `OPSRAMP_CLIENT_KEY` / `OPSRAMP_CLIENT_SECRET`. In
OpsRamp: **Setup → Integrations → (your agent integration)**:

- **`-K` access key** and **`-S` security key** (the agent's registration keys)
- **`-F` integration id**, e.g. `INTG-7a2a63b1-…` (also visible per host in the
  inventory as `attributes.installedIntgId`)

The API host (`-s`) comes from the connector automatically. The exact command
run on each host is:

```
sh /tmp/opsramp-deployAgent.sh -i silent -K <accessKey> -S <securityKey> \
   -s <api-host> -F <INTG-id> -L true
```

`-i silent` is mandatory: `deployAgent.sh` otherwise defaults to
`installType=interactive`, prints a banner, and blocks on a `read` for a y/n
confirmation. Over an SSH exec channel that read hits EOF immediately and the
script exits 1 having installed nothing.

**3 — Ensure SSH reachability.** You need an SSH user with `sudo` (password or
private key) on the targets. For private-subnet hosts, use a **jump host**
(bastion). Host keys are pinned on first use (TOFU) in
`$DEPLOY_STATE_DIR/known_hosts`; a key that later differs is rejected rather
than silently trusted.

**4 — Preflight, then install.** From the **Fleet Operations** view or the API
below. Always preflight first.

### Deploy API

```
POST /api/v1/deploy                 start an operation (returns the job)
GET  /api/v1/deploy/jobs            recent jobs
GET  /api/v1/deploy/jobs/{id}       job detail incl. per-host results
GET  /api/v1/reconcile              fleet drift report + remediation recommendations
```

`POST /api/v1/deploy` body (fields used depend on `action`):

```jsonc
{
  "action": "install",              // install|preflight|repair|upgrade|uninstall (default install)
  "targets": "10.0.0.10-10.0.0.20, 192.168.1.0/28, web-01.internal",
  "ssh_user": "root",
  "ssh_password": "…",              // or ssh_private_key (+ ssh_key_passphrase)
  "port": 22,
  "use_sudo": true,

  // install / repair / upgrade:
  "agent_key": "…",                 // -K  (Installed Integration access key)
  "agent_secret": "…",              // -S  (Installed Integration security key)
  "integration_id": "INTG-…",       // -F
  "enable_log_mgmt": true,          // -L true

  // uninstall:
  "deregister": false,              // also delete the resource from OpsRamp
  "uninstall_command": "",          // optional override of auto-detection

  // optional jump host (any action):
  "bastion_host": "", "bastion_user": "", "bastion_password": "",
  "bastion_private_key": "", "bastion_port": 22
}
```

**Targets** accept comma / whitespace / newline-separated tokens: single IPs or
hostnames, CIDR blocks (`10.0.0.0/28`), and ranges (`10.0.0.10-10.0.0.20` or the
short form `10.0.0.10-20`). Capped at 1024 hosts per job.

### Examples

```bash
BASE=http://localhost:4777

# 1) Preflight a subnet (read-only) — check what would happen before installing
curl -sX POST $BASE/api/v1/deploy -H 'content-type: application/json' -d '{
  "action":"preflight","targets":"10.0.0.0/28","ssh_user":"ubuntu","ssh_password":"…"}'

# 2) Install across a range
curl -sX POST $BASE/api/v1/deploy -H 'content-type: application/json' -d '{
  "action":"install","targets":"10.0.0.10-10.0.0.20","ssh_user":"root","ssh_password":"…",
  "agent_key":"<-K>","agent_secret":"<-S>","integration_id":"INTG-…","enable_log_mgmt":true}'

# 3) See what needs remediation, then repair the down agents
curl -s $BASE/api/v1/reconcile     # -> {"down":1,"outdated":47,"recommendations":[…]}
curl -sX POST $BASE/api/v1/deploy -H 'content-type: application/json' -d '{
  "action":"repair","targets":"10.0.0.13","ssh_user":"root","ssh_password":"…",
  "agent_key":"<-K>","agent_secret":"<-S>","integration_id":"INTG-…"}'

# 4) Uninstall + decommission (removes the agent AND the OpsRamp resource)
curl -sX POST $BASE/api/v1/deploy -H 'content-type: application/json' -d '{
  "action":"uninstall","targets":"10.0.0.15","ssh_user":"root","ssh_password":"…","deregister":true}'

# 5) Install through a bastion into a private subnet
curl -sX POST $BASE/api/v1/deploy -H 'content-type: application/json' -d '{
  "action":"install","targets":"10.20.0.0/28","ssh_user":"ec2-user","ssh_private_key":"…",
  "agent_key":"<-K>","agent_secret":"<-S>","integration_id":"INTG-…",
  "bastion_host":"bastion.example.com","bastion_user":"ec2-user","bastion_private_key":"…"}'
```

Poll per-host results with `GET /api/v1/deploy/jobs/{id}`. Preflight reports one
`KEY=VALUE` line per check — `SSH`, `OS`, `KERNEL`, `ARCH`, `SUDO`, `AGENT`,
`DISK_ROOT_MB`, `API` — which the UI grades as pass / warn / fail.

Implementation: SSH fan-out, TOFU host keys and per-action command building live
in `internal/deploy`; drift detection in `internal/reconcile`.

---

## OpAMP config reconciliation

1. Each `AgentToServer` message is persisted (identity, health, effective config,
   remote-config status, package status).
2. The agent is resolved to a group: explicit assignment → selector match →
   default group.
3. The server computes the group's current config hash and compares it to the
   hash the agent last reported. If they differ, the config is included in the
   response.
4. When an operator pushes a new config or package, the server **proactively
   pushes** the offer over the live WebSocket to every matching connected agent.
   HTTP-only agents pick it up on their next poll.

Config hashes are canonical — an order-independent sha256 over filename,
content-type and body — so the server and any agent always agree on whether the
config actually changed.

### Example: push a config, then roll it back

```bash
BASE=http://localhost:4777

curl -sX POST $BASE/api/v1/groups/default/config -H 'content-type: application/json' \
  -d '{"files":{"config.yaml":{"body":"log_level: info\n"}},"note":"v1"}'

curl -sX POST $BASE/api/v1/groups/default/config -H 'content-type: application/json' \
  -d '{"files":{"config.yaml":{"body":"log_level: debug\n"}},"note":"v2"}'

curl -sX POST $BASE/api/v1/groups/default/config/rollback -H 'content-type: application/json' \
  -d '{"version":1}'
```

---

## Admin API reference

Read endpoints are open; mutating calls require `ADMIN_AUTH_TOKEN` when it is
set (`Authorization: Bearer …`).

```
GET    /healthz | /readyz

GET    /api/v1/agents
GET    /api/v1/agents/{uid}
PUT    /api/v1/agents/{uid}/group             {"group":"canary"|null}
POST   /api/v1/agents/{uid}/push
GET    /api/v1/events

GET    /api/v1/groups
GET    /api/v1/groups/{name}
PUT    /api/v1/groups/{name}                  {"description":"…","selector":{"env":"prod"}}
DELETE /api/v1/groups/{name}
GET    /api/v1/groups/{name}/config
GET    /api/v1/groups/{name}/config/versions
POST   /api/v1/groups/{name}/config           {"files":{"config.yaml":{"body":"…"}},"note":"…"}
POST   /api/v1/groups/{name}/config/rollback  {"version":3}
GET    /api/v1/groups/{name}/packages
POST   /api/v1/groups/{name}/packages         {"package":"otelcol"}
DELETE /api/v1/groups/{name}/packages/{pkg}

GET    /api/v1/packages
POST   /api/v1/packages                       {"name":"otelcol","type":0,"version":"0.100.0","content_base64":"…"}
GET    /api/v1/packages/{name}
GET    /api/v1/packages/{name}/content        (download URL used by agents)
DELETE /api/v1/packages/{name}

       /api/v1/opsramp/…                      see the connector section
       /api/v1/deploy …  /api/v1/reconcile    see fleet operations
```

---

## Data model

Migrations are embedded in the binary and applied at startup
(`internal/store/migrations`).

| Table | Holds |
|-------|-------|
| `agents` | OpAMP agent registry: identity, attributes, labels, capabilities, health, effective config, status |
| `agent_events` | Append-only event log (connect, disconnect, config apply, failures) |
| `groups` | Group definitions and label selectors |
| `config_versions` | Immutable, versioned config maps per group (source of rollback) |
| `packages` | Uploaded package content and metadata |
| `group_packages` | Package ↔ group assignments |
| `opsramp_agents` | Inventory synced from the OpsRamp Resources API |
| `opsramp_config` | Single-row connector settings (base URL, tenant, key, secret, poll interval, enabled) |
| `deploy_jobs` | One row per fleet operation: action, targets, counts, status, timing |
| `deploy_host_results` | Per-host status, exit code, captured output and error |

---

## Security model

- **Agent auth** — set `OPAMP_AUTH_TOKEN` and agents must present it as a bearer
  token to connect.
- **Admin auth** — set `ADMIN_AUTH_TOKEN` and every non-`GET` admin call needs
  it. `GET`s stay open (agents fetch package content over one). Note that this
  means the OpsRamp token endpoints are gated but inventory reads are not.
- **SSH credentials** are held in memory for the life of a job, captured only by
  the closure that runs it, and are never written to the database or logs.
- **Host keys** are pinned on first use; a changed key fails the connection with
  a mismatch error instead of being silently accepted.
- **OpsRamp client secret** is stored in the database and never returned by the
  config endpoint — the UI shows only whether one is set.
- **The installer is left on target hosts** at `/tmp/opsramp-deployAgent.sh`
  (mode 700, owned by the SSH user). OpsRamp embeds tenant API keys in that
  script, so treat it as a secret if your hosts have untrusted local users.

---

## Troubleshooting

**Every OpsRamp call fails with `407` / `invalid_token`.**
The tenant rejected the access token. Check `GET /api/v1/opsramp/status` for
`authenticated` and `token_expires_at`, then force a new token with
`POST /api/v1/opsramp/token {"refresh":true}`. Remember that 407 — not 401 — is
how OpsRamp reports a bad token.

**Credentials look right but every tenant call fails.**
Use **Test connection**: it separates "bad key/secret" (401 `Bad API
credentials`) from "good credentials, wrong tenant id" (410 `No active
organization found with id …`).

**An install or repair fails within a second of connecting.**
The installer is exiting immediately. Confirm the command includes `-i silent`
(see [Fleet operations](#fleet-operations-over-ssh)) — without it,
`deployAgent.sh` blocks on an interactive prompt, gets EOF, and exits 1 without
writing `/tmp/opsramp-agent_install.log`. A successful SSH auth in the target's
`journalctl -u ssh` with an instantly closed session is the signature.

**A job shows `failed` but its host is stuck at `running` with no error.**
Fixed: captured SSH output is sanitized before storage (Postgres rejects NUL
bytes and invalid UTF-8), persistence failures are logged, and the verdict is
re-recorded without the output if the full result cannot be stored. If you see
this on an older build, upgrade.

**`agents/{platform}/info` returns `400 Invalid distName`.**
That endpoint wants a real distribution name, not `linux`.

**`agents/deployAgentsScript` returns 500.**
It requires a `scriptType` query parameter; the client sends `SHELL` (the other
valid value is `PYTHON`).

**The dashboard doesn't show a change you just deployed.**
Hard-reload once (`Ctrl+Shift+R` / `Cmd+Shift+R`). The page now sends an `ETag`
and `Cache-Control: no-cache`, so this should only bite on the upgrade to that
build.

**Deploy jobs suddenly report host-key mismatches after a redeploy.**
`DEPLOY_STATE_DIR` is not on a volume by default, so pins are lost when the
container is recreated. Mount a volume there.

---

## Development

```bash
make build            # -> bin/orchestrator, bin/demo-agent
make vet test         # go vet + unit tests
make tidy

# Run against a local Postgres:
docker run -d --name pg -e POSTGRES_USER=opamp -e POSTGRES_PASSWORD=opamp \
  -e POSTGRES_DB=opamp -p 5432:5432 postgres:16-alpine

export DATABASE_URL='postgres://opamp:opamp@localhost:5432/opamp?sslmode=disable'
./bin/orchestrator &                       # dashboard on :8080, OpAMP on :4320

OPAMP_SERVER_URL=ws://localhost:4320/v1/opamp AGENT_STATE_DIR=./agent-state ./bin/demo-agent
```

The dashboard is a single `go:embed`ed HTML file
(`internal/api/web/index.html`) — no build step and no external assets, so a UI
change means rebuilding the binary (`make up` rebuilds the image).

Tests cover target expansion, install command building, host-result persistence
fallbacks, OpAMP reconciliation, drift detection, and the OpsRamp client's OAuth,
token refresh, retry-on-`407`, and pagination behavior. SSH behavior against a
real server lives in `internal/deploy/ssh_integration_test.go`.

---

## Project layout

```
cmd/orchestrator      OpAMP server + admin API entrypoint
cmd/demo-agent        Reference OpAMP agent (applies config, syncs packages)
internal/config       Env configuration
internal/store        Store interface + Postgres impl + SQL migrations (embedded)
internal/opampserver  OpAMP callbacks, group resolution, hash reconciliation, push
internal/opsramp      OpsRamp REST client, OAuth token cache, connector, inventory poller
internal/deploy       SSH fan-out, TOFU host keys, per-action command building
internal/reconcile    Fleet drift detection and remediation recommendations
internal/api          Admin REST API + embedded dashboard
internal/model        Shared domain types
deploy/Dockerfile     Multi-target static build (distroless runtime)
scripts/seed.sh       Example config + package seeder
```
