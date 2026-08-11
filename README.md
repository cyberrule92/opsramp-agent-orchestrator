# OpsRamp Agent Orchestrator

A production-grade **OpAMP** (Open Agent Management Protocol) management server for
monitoring a fleet of agents. It accepts agent connections, persists their
reported state, and reconciles desired **remote configuration** and **packages**
back to them — with an operator REST API and a live dashboard.

Built on the canonical [`open-telemetry/opamp-go`](https://github.com/open-telemetry/opamp-go)
server library and backed by Postgres.

```
                 ┌───────────────────────── orchestrator ─────────────────────────┐
   OpAMP (ws)    │  ┌──────────────┐   reconcile   ┌───────────────┐               │
 agents ────────┼─▶│ OpAMP server │◀────────────▶ │  admin API/UI │◀── operators  │
   :4320        │  └──────┬───────┘               └───────┬───────┘   :8080        │
                 │         └──────────── store ────────────┘                       │
                 └──────────────────────────│─────────────────────────────────────┘
                                         Postgres
```

## Capabilities

| Area | What it does |
|------|--------------|
| **Agent registry** | Persistent identity, attributes, labels, capabilities, first/last seen, connection status. |
| **Health & status** | Tracks reported `ComponentHealth`, effective config, remote-config apply status, package status. |
| **Remote config** | Versioned, immutable config maps per **group**; hash-based reconciliation; instant push to connected agents; one-click **rollback**. |
| **Grouping** | Agents map to a group by explicit assignment or a **label selector**; otherwise the default group. |
| **Packages** | Upload agent binaries/addons, assign to groups, offer via OpAMP `PackagesAvailable`; agents download & report `PackageStatuses`. |
| **Observability** | Event log of every connect/disconnect/config-apply/failure; `/healthz` + `/readyz`. |
| **Fleet operations** | Install / preflight / repair / upgrade / uninstall the OpsRamp agent across a VM fleet over **agentless SSH** (IP / CIDR / range targets, jump-host support), plus continuous drift **reconciliation**. See [Fleet operations](#fleet-operations-agent-lifecycle-over-ssh). |
| **Security** | Optional bearer-token auth for agents (`OPAMP_AUTH_TOKEN`) and for mutating admin calls (`ADMIN_AUTH_TOKEN`). SSH credentials live only in memory for a run and are never persisted; host keys are pinned TOFU. |

## Quick start

```bash
cd opsramp-agent-orchestrator
make up          # builds images, starts postgres + orchestrator + 1 demo agent
make seed        # pushes an example config to the default group
open http://localhost:8080
```

Scale the fleet:

```bash
make scale-agents N=5
```

Tear down (with data):

```bash
make clean
```

## Layout

```
cmd/orchestrator     OpAMP server + admin API entrypoint
cmd/demo-agent       Reference OpAMP agent (applies config, syncs packages)
internal/config      Env configuration
internal/store       Store interface + Postgres impl + SQL migrations (embedded)
internal/opampserver OpAMP callbacks, group resolution, hash reconciliation, push
internal/api         Admin REST API + embedded dashboard
internal/model       Shared domain types
deploy/Dockerfile    Multi-target static build (distroless runtime)
```

## Configuration (env)

| Var | Default | Notes |
|-----|---------|-------|
| `DATABASE_URL` | *(required)* | pgx/libpq DSN |
| `OPAMP_LISTEN_ENDPOINT` | `:4320` | agent-facing listener |
| `OPAMP_LISTEN_PATH` | `/v1/opamp` | OpAMP path |
| `ADMIN_LISTEN_ENDPOINT` | `:8080` | admin API + UI |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | base for package download URLs handed to agents |
| `DEFAULT_GROUP` | `default` | fallback group |
| `OPAMP_AUTH_TOKEN` | *(empty)* | if set, agents must send `Authorization: Bearer …` |
| `ADMIN_AUTH_TOKEN` | *(empty)* | if set, mutating admin calls need the bearer token |
| `DEPLOY_STATE_DIR` | `/var/lib/orchestrator` | holds the TOFU `known_hosts` store for SSH fleet operations |
| `DEPLOY_CONCURRENCY` | `10` | max hosts operated on in parallel per job |
| `LOG_LEVEL` | `info` | debug/info/warn/error |

## Admin API

Read endpoints are open; mutations require `ADMIN_AUTH_TOKEN` when set.

```
GET    /healthz | /readyz
GET    /api/v1/agents
GET    /api/v1/agents/{uid}
PUT    /api/v1/agents/{uid}/group        {"group":"canary"|null}
POST   /api/v1/agents/{uid}/push
GET    /api/v1/events

GET    /api/v1/groups
PUT    /api/v1/groups/{name}             {"description":"…","selector":{"env":"prod"}}
DELETE /api/v1/groups/{name}
GET    /api/v1/groups/{name}/config
GET    /api/v1/groups/{name}/config/versions
POST   /api/v1/groups/{name}/config      {"files":{"config.yaml":{"body":"…"}},"note":"…"}
POST   /api/v1/groups/{name}/config/rollback  {"version":3}
GET    /api/v1/groups/{name}/packages
POST   /api/v1/groups/{name}/packages    {"package":"otelcol"}
DELETE /api/v1/groups/{name}/packages/{pkg}

GET    /api/v1/packages
POST   /api/v1/packages                  {"name":"otelcol","type":0,"version":"0.100.0","content_base64":"…"}
GET    /api/v1/packages/{name}
GET    /api/v1/packages/{name}/content   (download URL used by agents)
DELETE /api/v1/packages/{name}
```

### Example: push config, then roll back

```bash
BASE=http://localhost:8080

# push v1
curl -sX POST $BASE/api/v1/groups/default/config -H 'content-type: application/json' \
  -d '{"files":{"config.yaml":{"body":"log_level: info\n"}},"note":"v1"}'

# push v2 (bad)
curl -sX POST $BASE/api/v1/groups/default/config -H 'content-type: application/json' \
  -d '{"files":{"config.yaml":{"body":"log_level: debug\n"}},"note":"v2"}'

# roll back to v1
curl -sX POST $BASE/api/v1/groups/default/config/rollback -H 'content-type: application/json' \
  -d '{"version":1}'
```

## OpsRamp connector

OpsRamp agents are managed through the OpsRamp **REST/OAuth v2 API** — they do not
speak OpAMP. The orchestrator ships a first-class OpsRamp connector that
authenticates, **polls the agent inventory** (so OpsRamp agents show up in the
dashboard), and proxies the [`/v2/api/agents`](https://develop.opsramp.com/v2/api/agents)
management surface.

**Set it up from the dashboard** — no credentials need to live in the backend
config. Open **Inventory → Connector → Settings** and enter the API URL, tenant
id, and the client key/secret from an OpsRamp Integration:

- **Test connection** authenticates and probes the tenant *without saving*, and
  reports the two failure modes separately — bad key/secret, versus valid
  credentials paired with a tenant id the token cannot reach.
- **Get token** / **Force refresh** show the live Bearer token (hidden by
  default, with copy and a ready-made `curl` line) and when it expires, so an
  operator can reproduce any API call by hand.
- **Save & connect** validates, persists to the `opsramp_config` table, and
  hot-swaps the live client and poller — no restart.

The env vars below are optional: they only *seed* the initial config on first
boot when the database has none, and the stored settings win from then on.

```bash
OPSRAMP_API_URL=https://pod7.api.opsramp.com   # your pod's API host
OPSRAMP_TENANT_ID=client_xxxxxxxx              # tenant/client id
OPSRAMP_CLIENT_KEY=...                          # integration key
OPSRAMP_CLIENT_SECRET=...                       # integration secret
OPSRAMP_POLL_INTERVAL=60s
```

Auth flow: `POST {API_URL}/tenancy/auth/oauth/token` with
`grant_type=client_credentials` (form-encoded) → Bearer token (scope
`global:manage`, ~2h TTL, cached & auto-refreshed). API calls go to
`{API_URL}/api/v2/tenants/{tenantId}/...`.

**Token handling.** OpsRamp hands out a tenant-wide token and reports its
*remaining* life in `expires_in`, so the cached token is refreshed a minute
before it expires and is never trusted for longer than its 2h maximum
regardless of what the endpoint reports. Every agent operation — a deploy job,
an inventory sync, an admin proxy call — checks the token first and renews it if
expired, and any call the API rejects with `401`/`407 InvalidTokenException`
re-authenticates and retries once. `GET /api/v1/opsramp/status` reports
`token_expires_at` for the cached token.

Connector endpoints (all under the admin API):

```
GET  /api/v1/opsramp/config                          current settings (secret masked)
PUT  /api/v1/opsramp/config                          save settings and reconnect
POST /api/v1/opsramp/test                            validate credentials without saving them
POST /api/v1/opsramp/token                           current Bearer token ({"refresh":true} forces a new one)
GET  /api/v1/opsramp/status                         configured? authenticated? inventory count
GET  /api/v1/opsramp/agents                          synced agent inventory (from Resources Search)
POST /api/v1/opsramp/sync                            sync inventory now
GET  /api/v1/opsramp/agents/{platform}/info          proxy: agent package details
GET  /api/v1/opsramp/agents/{platform}/download/{pkg} proxy: download agent package (stream)
POST /api/v1/opsramp/updates                          proxy: configure agent auto-updates
POST /api/v1/opsramp/policies/{policyId}/devices      proxy: assign resources to an agent policy
POST /api/v1/opsramp/profiles/{profileId}/devices     proxy: assign resources to a master profile
```

`/test` and `/token` hand back a live Bearer token, and both are `POST` so that
`ADMIN_AUTH_TOKEN` gates them. Set that token on any deployment whose admin port
is reachable by anyone you would not hand tenant credentials to.

Inventory comes from the Resources Search API (`agentInstalled:true`), mapped to
`agentVersion` / `agentStatus` / host / IP and stored in `opsramp_agents`. The
dashboard renders it in the **OpsRamp Agents** panel with a "Sync now" button.
When credentials are absent the connector is disabled and these endpoints return
`503` with a clear message — the rest of the orchestrator runs normally.

Mapping of the documented `/v2/api/agents` surface → client methods lives in
`internal/opsramp/api.go`; OAuth + transport in `internal/opsramp/client.go`;
the inventory poller in `internal/opsramp/poller.go`.

## Fleet operations (agent lifecycle over SSH)

Beyond *monitoring* OpsRamp agents, the orchestrator can **act on the fleet** —
installing and managing the OpsRamp agent across many VMs at once over
**agentless SSH** (a self-contained Go fan-out; no Ansible or external tooling).
Everything is one pipeline distinguished by an **action**:

| Action | What it does | Needs connector? |
|--------|--------------|------------------|
| `preflight` | Read-only readiness probe per host: OS/arch, `sudo`, existing agent, root disk free, OpsRamp API reachability. Changes nothing. | no |
| `install` | Fetches OpsRamp `deployAgent.sh` (`scriptType=SHELL`) and runs it over SSH. | **yes** |
| `repair` | Re-runs the installer on hosts whose agent is **down** (restores it). | **yes** |
| `upgrade` | Re-runs the installer on hosts **behind the newest fleet version**. | **yes** |
| `uninstall` | Removes the agent (`dpkg -P` / `rpm -e` → `rm -rf /opt/opsramp/agent`); optionally **deregisters** the resource from OpsRamp. | only if deregistering |

A background **reconcile engine** continuously evaluates the inventory for down
and version-drifted agents and surfaces remediation **recommendations**
(approval-gated — applying one opens a targeted operation where you supply SSH
credentials, which are never stored).

### Setup from scratch

**1 — Bring up the stack.**

```bash
cd opsramp-agent-orchestrator
make up                                  # postgres + orchestrator
open http://localhost:4777               # dashboard (compose maps host 4777 → :8080)
```

**2 — Configure the OpsRamp connector** (required for install/repair/upgrade and
for inventory/reconcile). Either set the env vars from the [OpsRamp connector](#opsramp-connector)
section before `make up`, or do it live in the dashboard: **Inventory → Settings →
Save & connect**. Confirm with:

```bash
curl -s http://localhost:4777/api/v1/opsramp/status      # {"configured":true,"authenticated":true,...}
```

**3 — Get the agent install keys.** The installer's `-K` / `-S` are the **agent
access & security keys of an OpsRamp *Installed Integration*** — these are *not*
the REST OAuth `OPSRAMP_CLIENT_KEY/SECRET`. In OpsRamp: **Setup → Integrations →
(your agent integration)** to obtain:

- **`-K` access key** and **`-S` security key** (the agent's registration keys)
- **`-F` integration id**, e.g. `INTG-7a2a63b1-…` (also visible per host in the
  inventory as `attributes.installedIntgId`)

The API host (`-s`) is taken automatically from the connector's `OPSRAMP_API_URL`.
The final command the orchestrator runs on each host is exactly:

```
sh deployAgent.sh -K <accessKey> -S <securityKey> -s <api-host> -F <INTG-id> -L true
```

**4 — Ensure SSH reachability.** You need an SSH user with `sudo` (password or
private key) on the targets. For private-subnet hosts, use a **jump host**
(bastion). Host keys are pinned on first use (TOFU) in `DEPLOY_STATE_DIR/known_hosts`.

**5 — Dry-run with preflight, then install.** From the dashboard's **Fleet
Operations** view, or via the API below. Always preflight first.

### Deploy API

```
POST /api/v1/deploy                 start an operation (returns the job)
GET  /api/v1/deploy/jobs            recent jobs (with per-action badges)
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
curl -s $BASE/api/v1/reconcile        # -> {"down":1,"outdated":47,"recommendations":[{"action":"repair","target_spec":"…"}, …]}
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

Poll a job's per-host results with `GET /api/v1/deploy/jobs/{id}`; preflight jobs
report each check (reachable / sudo / agent / opsramp / disk) as pass·warn·fail.

Implementation: SSH fan-out, TOFU host keys, and per-action command building live
in `internal/deploy`; the reconcile engine (version drift + down detection) in
`internal/reconcile`.

## How OpAMP config reconciliation works

1. Each `AgentToServer` message is persisted (identity, health, effective config,
   remote-config status, package status).
2. The agent is resolved to a group (explicit assignment → selector match → default).
3. The server computes the group's current config hash and compares it to the hash
   the agent last reported. If they differ, it includes the config in the response.
4. When an operator pushes a new config/package, the server **proactively pushes**
   the offer over the live WebSocket to all matching connected agents. HTTP-only
   agents pick it up on their next poll.

Config hashes are canonical (order-independent sha256 over filename+content-type+body),
so the server and any agent always agree on whether config changed.

## Local development

```bash
make build      # -> bin/orchestrator, bin/demo-agent
make vet test

# Run against a local Postgres:
export DATABASE_URL='postgres://opamp:opamp@localhost:5432/opamp?sslmode=disable'
./bin/orchestrator &
OPAMP_SERVER_URL=ws://localhost:4320/v1/opamp AGENT_STATE_DIR=./agent-state ./bin/demo-agent
```
