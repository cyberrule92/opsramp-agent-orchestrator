#!/usr/bin/env bash
# Seed the orchestrator with an example group config and package.
# Usage: ./scripts/seed.sh [BASE_URL]
set -euo pipefail

BASE="${1:-http://localhost:8080}"
TOKEN_HEADER=()
if [[ -n "${ADMIN_AUTH_TOKEN:-}" ]]; then
  TOKEN_HEADER=(-H "Authorization: Bearer ${ADMIN_AUTH_TOKEN}")
fi

echo ">> Pushing example config to 'default' group ..."
curl -fsS "${TOKEN_HEADER[@]}" -X POST "${BASE}/api/v1/groups/default/config" \
  -H 'Content-Type: application/json' \
  -d '{
    "note": "seed: baseline monitoring config",
    "files": {
      "config.yaml": {
        "content_type": "text/yaml",
        "body": "receivers:\n  hostmetrics:\n    collection_interval: 30s\nexporters:\n  otlp:\n    endpoint: collector:4317\nservice:\n  pipelines:\n    metrics:\n      receivers: [hostmetrics]\n      exporters: [otlp]\n"
      }
    }
  }' | sed 's/^/   /'
echo

echo ">> Creating a 'canary' group selecting env=prod ..."
curl -fsS "${TOKEN_HEADER[@]}" -X PUT "${BASE}/api/v1/groups/canary" \
  -H 'Content-Type: application/json' \
  -d '{"description":"prod canary","selector":{"env":"prod"}}' | sed 's/^/   /'
echo

echo ">> Done. Open ${BASE} to view the dashboard."
