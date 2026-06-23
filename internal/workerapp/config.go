package workerapp

import (
	"strings"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	MasterGRPCAddr     string `env:"MASTER_GRPC_ADDR" envDefault:"localhost:9000"`
	WorkerID           string `env:"WORKER_ID"`
	Concurrency        int32  `env:"WORKER_CONCURRENCY" envDefault:"4"`
	WorkerSharedSecret string `env:"WORKER_SHARED_SECRET,required"`

	// MinIO — the worker downloads spider sources from here.
	MinIOEndpoint  string `env:"MINIO_ENDPOINT,required"`
	MinIOAccessKey string `env:"MINIO_ACCESS_KEY,required"`
	MinIOSecretKey string `env:"MINIO_SECRET_KEY,required"`
	MinIOBucket    string `env:"MINIO_BUCKET" envDefault:"crawler-artifacts"`
	MinIOSecure    bool   `env:"MINIO_SECURE" envDefault:"false"`

	// Path to the Python interpreter used to spawn `python -m crawlerkit.runner`.
	PythonPath string `env:"PYTHON_PATH" envDefault:"python3"`

	// Parent dir for per-task working dirs. Each task lands in a fresh subdir.
	WorkDir string `env:"WORKER_WORKDIR" envDefault:"/tmp/crawler-lite"`

	// Parent dir for per-spider venvs, keyed by requirements.txt hash. Unlike
	// WorkDir this should persist across reboots — its whole purpose is to
	// avoid reinstalling deps on every task. ~5 MB per venv with uv's
	// hard-linked wheel cache, so it's cheap.
	VenvDir string `env:"WORKER_VENV_DIR" envDefault:"/var/lib/crawler-lite/venvs"`

	// Path to `uv` (Astral's pip-compatible installer). Empty → look it up on
	// PATH at startup. If unset and not found on PATH, per-spider
	// requirements.txt is skipped (with a warning); dep-free spiders still work.
	UVPath string `env:"UV_PATH" envDefault:""`

	// Comma-separated, e.g. "python3.12,chromium,selenium"
	CapabilitiesRaw string `env:"WORKER_CAPABILITIES" envDefault:"python3.12"`
}

func (c Config) Capabilities() []string {
	parts := strings.Split(c.CapabilitiesRaw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func LoadConfig() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
