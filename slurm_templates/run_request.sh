#!/usr/bin/env bash
# run_request.sh — Slurm array task
#
# Backpressure: polls vLLM /metrics directly (no gateway overhead).
# Inference:    POSTs through APIsix for uniform gateway logging.
#
# Env vars injected by sbatch --env:
#   BATCH_ID          — shim batch ID for log correlation
#   APISIX_URL        — internal gateway address e.g. http://apisix:9080
#   BATCH_API_KEY     — dedicated API key issued to the shim in APIsix
#   VLLM_METRICS_MAP  — "model=http://host:port,..." for direct metrics polling
#   VLLM_MAX_RUNNING  — hold off if target model has >= this many running requests
#   VLLM_WAIT_TIMEOUT — give up after N seconds (triggers Slurm --requeue)

set -euo pipefail

INPUT_FILE="$1"
OUTPUT_DIR="$2"
ENDPOINT="${3:-/v1/chat/completions}"
TASK_ID="${SLURM_ARRAY_TASK_ID}"

MAX_RUNNING="${VLLM_MAX_RUNNING:-4}"
WAIT_TIMEOUT="${VLLM_WAIT_TIMEOUT:-300}"
POLL_INTERVAL=5

log() { echo "[batch=${BATCH_ID:-?} task=${TASK_ID}] $*"; }
err() { echo "[batch=${BATCH_ID:-?} task=${TASK_ID}] ERROR: $*" >&2; }

# ── 1. Extract JSONL line ─────────────────────────────────────────────────────

LINE_NUM=$(( TASK_ID + 1 ))
REQUEST_LINE=$(sed -n "${LINE_NUM}p" "$INPUT_FILE")

if [[ -z "$REQUEST_LINE" ]]; then
    err "no line at index ${TASK_ID}"
    exit 1
fi

IFS=$'\t' read -r CUSTOM_ID MODEL BODY <<< "$(python3 -c "
import sys, json
d = json.loads(sys.argv[1])
print(d.get('custom_id',''), d.get('body',{}).get('model',''), json.dumps(d.get('body',{})), sep='\t')
" "$REQUEST_LINE")"

log "custom_id=${CUSTOM_ID} model=${MODEL}"

# ── 2. Resolve vLLM metrics URL from model map ────────────────────────────────
# VLLM_METRICS_MAP="qwen=http://vllm-qwen:8000,gemma=http://vllm-gemma:8000,..."
# We only need this for /metrics polling — inference goes through APIsix.

VLLM_METRICS_URL=""
IFS=',' read -ra PAIRS <<< "${VLLM_METRICS_MAP:-}"
for PAIR in "${PAIRS[@]}"; do
    KEY="${PAIR%%=*}"
    VAL="${PAIR#*=}"
    if [[ "$KEY" == "$MODEL" ]]; then
        VLLM_METRICS_URL="${VAL}/metrics"
        break
    fi
done

if [[ -z "$VLLM_METRICS_URL" ]]; then
    # Model not in map — fall back to first entry
    FIRST="${VLLM_METRICS_MAP%%,*}"
    VLLM_METRICS_URL="${FIRST#*=}/metrics"
    log "WARN: model '${MODEL}' not in VLLM_METRICS_MAP, falling back to ${VLLM_METRICS_URL}"
fi

# ── 3. Backpressure — poll vLLM /metrics directly ────────────────────────────
# Direct hit to vLLM, no gateway involved. Keeps metrics traffic off APIsix
# logs and avoids any auth overhead on what is essentially an internal health check.

wait_for_capacity() {
    local elapsed=0
    while true; do
        RUNNING=$(curl -sf --max-time 3 "${VLLM_METRICS_URL}" 2>/dev/null \
            | grep -E "^vllm:num_requests_running\{[^}]*model=\"${MODEL}\"" \
            | awk '{print int($2)}' \
            | head -1 || echo "0")
        RUNNING=${RUNNING:-0}

        if [[ "$RUNNING" -lt "$MAX_RUNNING" ]]; then
            log "capacity OK (model=${MODEL} running=${RUNNING} < max=${MAX_RUNNING})"
            return 0
        fi

        log "waiting (model=${MODEL} running=${RUNNING} >= max=${MAX_RUNNING}, elapsed=${elapsed}s)"

        if [[ "$elapsed" -ge "$WAIT_TIMEOUT" ]]; then
            err "capacity wait timed out after ${elapsed}s — requeueing"
            exit 1
        fi

        sleep "$POLL_INTERVAL"
        elapsed=$(( elapsed + POLL_INTERVAL ))
    done
}

wait_for_capacity

# ── 4. POST through APIsix ────────────────────────────────────────────────────
# Identical to any API client. APIsix validates the key, logs the request,
# and routes to the correct vLLM instance based on the model field.

OUTPUT_FILE="${OUTPUT_DIR}/${TASK_ID}.json"
TMP_FILE="${OUTPUT_FILE}.tmp"

HTTP_STATUS=$(curl -s \
    -o "${TMP_FILE}" \
    -w "%{http_code}" \
    --max-time 120 \
    -X POST "${APISIX_URL}${ENDPOINT}" \
    -H "Content-Type: application/json" \
    -H "apikey: ${BATCH_API_KEY}" \
    -d "${BODY}")

# ── 5. Wrap result in BatchResponseLine format ─────────────────────────────────

if [[ "$HTTP_STATUS" == "200" ]]; then
    python3 - <<PYEOF
import json

with open("${TMP_FILE}") as f:
    resp = json.load(f)

result = {
    "id": "resp-${TASK_ID}",
    "custom_id": "${CUSTOM_ID}",
    "response": {
        "status_code": 200,
        "request_id": resp.get("id", ""),
        "body": resp,
    },
    "error": None,
}

with open("${OUTPUT_FILE}", "w") as f:
    json.dump(result, f)

usage = resp.get("usage", {})
print(f"[batch=${BATCH_ID} task=${TASK_ID}] OK tokens={usage.get('total_tokens','?')}")
PYEOF

else
    python3 - <<PYEOF
import json

error_body = {}
try:
    with open("${TMP_FILE}") as f:
        error_body = json.load(f)
except Exception:
    pass

result = {
    "id": "resp-${TASK_ID}",
    "custom_id": "${CUSTOM_ID}",
    "response": None,
    "error": {
        "code": "gateway_error",
        "message": error_body.get("message", "HTTP ${HTTP_STATUS}"),
    },
}

with open("${OUTPUT_FILE}", "w") as f:
    json.dump(result, f)
PYEOF
    err "gateway returned HTTP ${HTTP_STATUS}"
    exit 1
fi

rm -f "${TMP_FILE}"
log "done"
