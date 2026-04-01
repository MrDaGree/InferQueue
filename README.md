# batch-shim — dev environment

## Prerequisites
- Docker Desktop
- VSCode with the **Dev Containers** extension

## Getting started

1. Open this folder in VSCode
2. When prompted, click **Reopen in Container** (or run `Dev Containers: Reopen in Container` from the command palette)
3. The container builds, Postgres starts, and `init-slurm.sh` runs — this takes ~60 seconds the first time
4. Once the terminal shows `Dev environment ready`, Slurm is running with three partitions:
   - `batch-high` — priority 40, ≤ 36h window
   - `batch-medium` — priority 30, ≤ 72h window
   - `batch-low` — priority 10, unlimited

## Running the shim

```bash
# In the devcontainer terminal:
go mod tidy
go run ./cmd/server
```

## Running the mock APIsix (separate terminal)

The shim's Slurm tasks POST requests to `APISIX_URL` (default `http://localhost:9080`).
In dev there's no real APIsix, so use the included mock:

```bash
bash slurm_templates/mock-apisix.sh
```

It listens on `:9080` and returns a valid chat completion for every POST.

## Testing

Open `sample.http` in VSCode and use the REST Client extension to send requests.
Replace `FILE_ID` and `BATCH_ID` placeholders with real IDs from responses.

## Verifying Slurm

```bash
sinfo                          # show partitions and node state
squeue                         # running/pending jobs
sacct -j <job_id> --format=JobID,State,ExitCode --noheader --parsable2
sbatch --partition=batch-high --wrap='echo hello'
```

If the node shows as `down` after a container rebuild:
```bash
scontrol update nodename=devbox state=resume
```

## Module path

The module is `inferqueue`. Update `go.mod` and all import
paths if you rename it.

## Environment variables

All config is in `.devcontainer/docker-compose.yml` under the `devbox` service environment.
Key ones for local dev:

| Variable | Dev default | Notes |
|---|---|---|
| `APISIX_URL` | `http://localhost:9080` | Points at mock-apisix.sh |
| `BATCH_API_KEY` | `dev-key` | Any string works against the mock |
| `VLLM_MAX_RUNNING_REQUESTS` | `999` | Disables backpressure |
| `VLLM_METRICS_MAP` | `test-model=http://localhost:8000` | Not used when backpressure disabled |
