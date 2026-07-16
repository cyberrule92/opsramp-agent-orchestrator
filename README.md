# OpAMP Orchestrator

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
| **Security** | Optional bearer-token auth for agents (`OPAMP_AUTH_TOKEN`) and for mutating admin calls (`ADMIN_AUTH_TOKEN`). |

## Quick start

```bash
cd /opt/opamp-orchestrator
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

Enable it by providing credentials from an OpsRamp Integration (OAuth2 client
credentials):

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

Connector endpoints (all under the admin API):

```
GET  /api/v1/opsramp/status                         configured? authenticated? inventory count
GET  /api/v1/opsramp/agents                          synced agent inventory (from Resources Search)
POST /api/v1/opsramp/sync                            sync inventory now
GET  /api/v1/opsramp/agents/{platform}/info          proxy: agent package details
GET  /api/v1/opsramp/agents/{platform}/download/{pkg} proxy: download agent package (stream)
POST /api/v1/opsramp/updates                          proxy: configure agent auto-updates
POST /api/v1/opsramp/policies/{policyId}/devices      proxy: assign resources to an agent policy
POST /api/v1/opsramp/profiles/{profileId}/devices     proxy: assign resources to a master profile
```

Inventory comes from the Resources Search API (`agentInstalled:true`), mapped to
`agentVersion` / `agentStatus` / host / IP and stored in `opsramp_agents`. The
dashboard renders it in the **OpsRamp Agents** panel with a "Sync now" button.
When credentials are absent the connector is disabled and these endpoints return
`503` with a clear message — the rest of the orchestrator runs normally.

Mapping of the documented `/v2/api/agents` surface → client methods lives in
`internal/opsramp/api.go`; OAuth + transport in `internal/opsramp/client.go`;
the inventory poller in `internal/opsramp/poller.go`.

## How reconciliation works

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
