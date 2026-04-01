package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port string
	DSN  string

	FileStore string

	// Batch tasks POST back through APIsix rather than calling vLLM directly.
	// This gives uniform gateway logging for all traffic — interactive and batch
	// go through the same audit path with no extra instrumentation in the shim.
	//
	// APIsixURL is the internal address of the gateway (behind Nginx TLS, or
	// direct to APIsix if tasks run inside the same Docker network).
	// BatchAPIKey is a dedicated key issued in APIsix for the shim service;
	// APIsix will inject X-User-Id=batch-shim (or whatever the key metadata says)
	// so batch traffic is distinguishable from interactive traffic in logs.
	APIsixURL   string
	BatchAPIKey string

	// VLLMMetricsMap is model→http://host:port for direct Prometheus /metrics polling.
	// Only used for backpressure checks — inference still routes through APIsix.
	// e.g. "qwen=http://vllm-qwen:8000,gemma=http://vllm-gemma:8000,lora=http://vllm-lora:8000"
	VLLMMetricsMap         map[string]string

	// Backpressure: task waits until the target vLLM has fewer than this many running requests.
	VLLMMaxRunningRequests int
	VLLMWaitTimeoutSec     int

	// Slurm partitions
	SlurmPartitionSmall    string
	SlurmPartitionNormal   string
	SlurmPartitionLarge    string
	SlurmPartitionDeferred string

	SlurmLogDir    string
	SlurmJobScript string

	TrustProxyHeaders bool
	MaxFileAgeDays    int
}

func Load() *Config {
	return &Config{
		Port: getEnv("PORT", "8080"),
		DSN:  getEnv("DATABASE_DSN", "host=postgres user=batchshim password=batchshim dbname=batchshim port=5432 sslmode=disable"),

		FileStore: getEnv("FILE_STORE", "/data/files"),

		// Tasks POST to APIsix — same path as any API client.
		// Set APISIX_URL to the internal gateway address reachable from the Slurm node.
		APIsixURL:   getEnv("APISIX_URL", "http://apisix:9080"),
		BatchAPIKey: getEnv("BATCH_API_KEY", ""),

		VLLMMetricsMap: parseModelMap(getEnv("VLLM_METRICS_MAP",
			"qwen=http://vllm-qwen:8000,gemma=http://vllm-gemma:8000,lora=http://vllm-lora:8000")),

		VLLMMaxRunningRequests: getEnvInt("VLLM_MAX_RUNNING_REQUESTS", 4),
		VLLMWaitTimeoutSec:     getEnvInt("VLLM_WAIT_TIMEOUT_SEC", 300),

		SlurmPartitionSmall:    getEnv("SLURM_PARTITION_SMALL", "batch-high"),
		SlurmPartitionNormal:   getEnv("SLURM_PARTITION_NORMAL", "batch-medium"),
		SlurmPartitionLarge:    getEnv("SLURM_PARTITION_LARGE", "batch-medium"),
		SlurmPartitionDeferred: getEnv("SLURM_PARTITION_DEFERRED", "batch-low"),

		SlurmLogDir:    getEnv("SLURM_LOG_DIR", "/data/slurm-logs"),
		SlurmJobScript: getEnv("SLURM_JOB_SCRIPT", "/app/slurm_templates/run_request.sh"),

		TrustProxyHeaders: getEnvBool("TRUST_PROXY_HEADERS", true),
		MaxFileAgeDays:    getEnvInt("MAX_FILE_AGE_DAYS", 7),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func parseModelMap(raw string) map[string]string {
	m := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			m[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return m
}
