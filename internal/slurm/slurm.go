package slurm

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"inferqueue/internal/config"
	"inferqueue/internal/models"
)

var jobIDRe = regexp.MustCompile(`Submitted batch job (\d+)`)

// ── Scheduling strategy ───────────────────────────────────────────────────────
//
// Partition selection is driven purely by the effective window duration and
// request count. Thresholds:
//
//   d <= 36h   high-urgency path — partition chosen by request count:
//                < 10 requests  → batch-rt      (realtime, highest priority)
//                10–99          → batch-normal
//                100+           → overnight
//
//   36h < d <= 72h   relaxed path → batch-normal (Slurm backfills opportunistically)
//
//   d > 72h          deferred path → batch-deferred (lowest priority, idle gaps only)
//
// All submissions carry a hard --deadline so Slurm expires tasks that cannot
// complete in time rather than running them after the client has given up.

type schedulingStrategy struct {
	partition string
	deadline  time.Time
}

func buildStrategy(cfg *config.Config, requestCount int, windowDur time.Duration, deadline time.Time) schedulingStrategy {
	var partition string
	switch {
	case windowDur > 72*time.Hour:
		partition = cfg.SlurmPartitionDeferred
	case windowDur > 36*time.Hour:
		partition = cfg.SlurmPartitionNormal
	default: // <= 36h — pick by request count
		switch {
		case requestCount >= 100:
			partition = cfg.SlurmPartitionLarge
		case requestCount >= 10:
			partition = cfg.SlurmPartitionNormal
		default:
			partition = cfg.SlurmPartitionSmall
		}
	}
	return schedulingStrategy{partition: partition, deadline: deadline}
}

// PickPartition is a convenience wrapper used in tests and logging.
func PickPartition(cfg *config.Config, requestCount int, windowDur time.Duration, deadline time.Time) string {
	return buildStrategy(cfg, requestCount, windowDur, deadline).partition
}

// ── Job submission ────────────────────────────────────────────────────────────

type SubmitParams struct {
	BatchID            string
	InputFilePath      string
	OutputDir          string
	RequestCount       int
	Endpoint           string
	WindowDur          time.Duration // effective duration after clamping
	Deadline           time.Time
	APIsixURL          string
	BatchAPIKey        string
	VLLMMetricsMap     string
	MaxRunningRequests int
	WaitTimeoutSec     int
}

func SubmitJob(cfg *config.Config, p SubmitParams) (string, error) {
	strategy := buildStrategy(cfg, p.RequestCount, p.WindowDur, p.Deadline)

	logDir := fmt.Sprintf("%s/%s", cfg.SlurmLogDir, p.BatchID)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", fmt.Errorf("create slurm log dir: %w", err)
	}
	if err := os.MkdirAll(p.OutputDir, 0o755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	// Pass task env vars via the process environment + --export=ALL.
	// --env was only added in Slurm 21.x; Slurm 20.11 (Debian bullseye)
	// inherits the calling process environment when --export=ALL is set.
	taskEnv := map[string]string{
		"BATCH_ID":          p.BatchID,
		"APISIX_URL":        p.APIsixURL,
		"BATCH_API_KEY":     p.BatchAPIKey,
		"VLLM_METRICS_MAP":  p.VLLMMetricsMap,
		"VLLM_MAX_RUNNING":  fmt.Sprintf("%d", p.MaxRunningRequests),
		"VLLM_WAIT_TIMEOUT": fmt.Sprintf("%d", p.WaitTimeoutSec),
	}

	args := []string{
		"--job-name=batch-shim-" + p.BatchID,
		"--partition=" + strategy.partition,
		fmt.Sprintf("--array=0-%d", p.RequestCount-1),
		fmt.Sprintf("--output=%s/task-%%a.out", logDir),
		fmt.Sprintf("--error=%s/task-%%a.err", logDir),
		"--export=ALL",
		"--requeue",
	}

	if !strategy.deadline.IsZero() {
		args = append(args, "--deadline="+strategy.deadline.Format("2006-01-02T15:04:05"))
	}

	args = append(args, cfg.SlurmJobScript, p.InputFilePath, p.OutputDir, p.Endpoint)

	cmd := exec.Command("sbatch", args...)
	// Inject task-specific vars into sbatch's environment so --export=ALL
	// passes them through to each array task.
	cmd.Env = os.Environ()
	for k, v := range taskEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	slog.Info("sbatch",
		"partition", strategy.partition,
		"window", p.WindowDur.String(),
		"deadline", strategy.deadline.Format(time.RFC3339),
		"tasks", p.RequestCount,
	)

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("sbatch: %w", err)
	}

	m := jobIDRe.FindSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("unexpected sbatch output: %q", out)
	}

	jobID := string(m[1])
	slog.Info("slurm job submitted",
		"job_id", jobID,
		"batch_id", p.BatchID,
		"partition", strategy.partition,
		"window", p.WindowDur.String(),
		"tasks", p.RequestCount,
	)
	return jobID, nil
}

// ── Status polling ────────────────────────────────────────────────────────────

type JobStatus struct {
	Overall   models.BatchStatus
	Total     int
	Completed int
	Failed    int
}

// PollJob checks job status via sacct, falling back to squeue when Slurm
// accounting is disabled (common in dev/single-node setups).
// If the job is no longer visible in either, we count output files on disk
// to determine completion — this is what makes the dev loop work end-to-end.
func PollJob(jobID string) (JobStatus, error) {
	// ── Try sacct first ───────────────────────────────────────────────────
	out, err := exec.Command(
		"sacct", "-j", jobID,
		"--format=JobID,State,ExitCode",
		"--noheader", "--parsable2",
	).Output()
	if err == nil {
		var total, completed, failed int
		allDone := true
		hasFinalizing := false

		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "|")
			if !strings.Contains(parts[0], "_") {
				continue
			}
			state := "UNKNOWN"
			if len(parts) > 1 {
				state = strings.Fields(parts[1])[0]
			}
			total++
			switch state {
			case "COMPLETED":
				completed++
			case "FAILED", "CANCELLED", "TIMEOUT", "NODE_FAIL", "DEADLINE":
				failed++
			case "COMPLETING":
				hasFinalizing = true
				allDone = false
			default:
				allDone = false
			}
		}

		if total > 0 {
			var overall models.BatchStatus
			switch {
			case allDone && failed == total:
				overall = models.StatusFailed
			case allDone:
				overall = models.StatusCompleted
			case hasFinalizing:
				overall = models.StatusFinalizing
			default:
				overall = models.StatusInProgress
			}
			return JobStatus{Overall: overall, Total: total, Completed: completed, Failed: failed}, nil
		}
	}

	// ── Fall back: check squeue ───────────────────────────────────────────
	// If sacct returned nothing (accounting disabled), check whether the job
	// is still queued or running.
	sqOut, sqErr := exec.Command("squeue", "-j", jobID, "--noheader").Output()
	if sqErr == nil && strings.TrimSpace(string(sqOut)) != "" {
		// Job still visible in squeue — still running
		return JobStatus{Overall: models.StatusInProgress}, nil
	}

	// Job not in squeue and sacct empty — job finished.
	// Return StatusCompleted with zero counts; the assembler will count files.
	return JobStatus{Overall: models.StatusCompleted, Total: 0, Completed: 0, Failed: 0}, nil
}

func CancelJob(jobID string) error {
	out, err := exec.Command("scancel", jobID).CombinedOutput()
	if err != nil {
		return fmt.Errorf("scancel %s: %w — %s", jobID, err, out)
	}
	slog.Info("slurm job cancelled", "job_id", jobID)
	return nil
}
