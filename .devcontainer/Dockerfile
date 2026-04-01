# bullseye (Debian 11) ships Slurm 20.11 which honours ProctrackType=linuxproc
# without mandatory cgroup initialisation. bookworm's Slurm 23.x added
# unconditional cgroup setup for step daemons that breaks in Docker.
FROM golang:1.23-bullseye

# ── System packages ───────────────────────────────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
    slurmd \
    slurmctld \
    slurm-client \
    munge \
    libmunge-dev \
    python3 \
    curl \
    bash \
    jq \
    postgresql-client \
    git \
    vim \
    && rm -rf /var/lib/apt/lists/*

# ── Slurm directories ─────────────────────────────────────────────────────────
RUN mkdir -p \
    /var/spool/slurmctld \
    /var/spool/slurmd \
    /var/log/slurm \
    /var/run/slurm \
    /etc/slurm \
    && chown -R slurm:slurm /var/spool/slurmctld /var/spool/slurmd /var/log/slurm /var/run/slurm

# ── Slurm config ──────────────────────────────────────────────────────────────
# No cgroup.conf needed — Slurm 20.11 with ProctrackType=linuxproc does not
# require cgroup access.
COPY slurm.conf /etc/slurm/slurm.conf

# ── Go dev tools ──────────────────────────────────────────────────────────────
# Both tools have outpaced Go 1.23 — pin to the last compatible releases:
#   gopls  v0.16.2 — last release targeting go 1.23
#   dlv    v1.23.1 — last release targeting go 1.23
RUN go install golang.org/x/tools/gopls@v0.16.2 \
    && go install github.com/go-delve/delve/cmd/dlv@v1.23.1

WORKDIR /workspace