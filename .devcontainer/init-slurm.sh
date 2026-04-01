#!/usr/bin/env bash
# .devcontainer/init-slurm.sh
# Starts munge, slurmctld, slurmd and verifies the three partitions are up.

set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

log()  { echo -e "${GREEN}[slurm-init]${NC} $*"; }
warn() { echo -e "${YELLOW}[slurm-init]${NC} $*"; }
fail() {
    echo -e "${RED}[slurm-init]${NC} $*"
    echo ""
    echo -e "${RED}── slurmctld.log ──${NC}"
    tail -20 /var/log/slurm/slurmctld.log 2>/dev/null || echo "(empty)"
    echo -e "${RED}── slurmd.log ─────${NC}"
    tail -20 /var/log/slurm/slurmd.log 2>/dev/null || echo "(empty)"
    exit 1
}

# ── 1. Patch slurm.conf with actual node hardware ─────────────────────────────
# slurmd refuses to start if CPUs/RealMemory in slurm.conf exceed reality.
# We detect the real values and patch the file every time so rebuilds just work.
log "Detecting node hardware..."
ACTUAL=$(slurmd -C 2>/dev/null | head -1)
ACTUAL_CPUS=$(echo "$ACTUAL"    | grep -oP 'CPUs=\K[0-9]+')
ACTUAL_MEM=$(echo "$ACTUAL"     | grep -oP 'RealMemory=\K[0-9]+')

if [[ -n "$ACTUAL_CPUS" && -n "$ACTUAL_MEM" ]]; then
    log "  Hardware: CPUs=${ACTUAL_CPUS} RealMemory=${ACTUAL_MEM}"
    sed -i "s/NodeName=devbox CPUs=[0-9]* RealMemory=[0-9]*/NodeName=devbox CPUs=${ACTUAL_CPUS} RealMemory=${ACTUAL_MEM}/" \
        /etc/slurm/slurm.conf
    log "  slurm.conf patched"
else
    warn "  Could not detect hardware — using values from slurm.conf as-is"
fi

# ── 2. Munge ──────────────────────────────────────────────────────────────────
log "Setting up munge..."
mkdir -p /etc/munge /var/run/munge /var/log/munge /var/lib/munge
chown -R munge:munge /etc/munge /var/run/munge /var/log/munge /var/lib/munge

if [[ ! -f /etc/munge/munge.key ]]; then
    log "  Generating munge key..."
    dd if=/dev/urandom bs=1 count=1024 > /etc/munge/munge.key 2>/dev/null
fi
chmod 400 /etc/munge/munge.key
chown munge:munge /etc/munge/munge.key

if ! pgrep -x munged > /dev/null; then
    su -s /bin/bash munge -c "munged --force"
    sleep 2
fi

munge -n | unmunge > /dev/null 2>&1 || fail "munge authentication is not working"
log "munge OK"

# ── 3. Directories ────────────────────────────────────────────────────────────
log "Preparing Slurm state directories..."
mkdir -p /var/spool/slurmctld /var/spool/slurmd /var/log/slurm /var/run/slurm \
         /data/slurm-logs /data/files
chown -R slurm:slurm /var/spool/slurmctld /var/spool/slurmd /var/log/slurm /var/run/slurm
touch /var/log/slurm/slurmctld.log /var/log/slurm/slurmd.log
chown slurm:slurm /var/log/slurm/slurmctld.log /var/log/slurm/slurmd.log

# ── 4. slurmctld ──────────────────────────────────────────────────────────────
log "Starting slurmctld..."
if ! pgrep -x slurmctld > /dev/null; then
    slurmctld
    sleep 3
fi
pgrep -x slurmctld > /dev/null || fail "slurmctld failed to start"
log "slurmctld OK"

# ── 5. slurmd ─────────────────────────────────────────────────────────────────
log "Starting slurmd..."
if ! pgrep -x slurmd > /dev/null; then
    slurmd -N devbox
    sleep 3
fi
pgrep -x slurmd > /dev/null || fail "slurmd failed to start"
log "slurmd OK"

# ── 6. Resume node ────────────────────────────────────────────────────────────
log "Resuming node..."
sleep 2
scontrol update nodename=devbox state=resume 2>/dev/null || true
sleep 2

# ── 7. Verify ─────────────────────────────────────────────────────────────────
log "Verifying partitions..."
for p in batch-high batch-medium batch-low; do
    if sinfo --noheader --format="%P" 2>/dev/null | tr -d '*' | grep -q "^${p}$"; then
        log "  partition ${p} ✓"
    else
        warn "  partition ${p} not visible yet"
    fi
done

NODE_STATE=$(scontrol show node devbox 2>/dev/null | grep -oP 'State=\S+' | head -1)
log "Node state: ${NODE_STATE}"

if echo "$NODE_STATE" | grep -qi "drain"; then
    REASON=$(scontrol show node devbox 2>/dev/null | grep -oP 'Reason=\K[^\n]+' | head -1)
    warn "Node is draining — Reason: ${REASON}"
    warn "Run: scontrol update nodename=devbox state=resume"
fi

echo ""
log "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
log "Dev environment ready."
log ""
log "Run the shim:   cd /workspace && go run ./cmd/server"
log "Mock APIsix:    bash slurm_templates/mock-apisix.sh"
log "Check jobs:     squeue"
log "Node status:    scontrol show node devbox"
log "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"