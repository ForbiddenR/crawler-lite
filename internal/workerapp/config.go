package workerapp

import (
	"strings"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	MasterGRPCAddr     string `env:"MASTER_GRPC_ADDR" envDefault:"localhost:9000"`
	WorkerID           string `env:"WORKER_ID,required"`
	Concurrency        int32  `env:"WORKER_CONCURRENCY" envDefault:"4"`
	WorkerSharedSecret string `env:"WORKER_SHARED_SECRET,required"`

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
