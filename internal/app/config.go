package app

import (
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is the master process configuration. Populated from environment
// variables; see .env.example for defaults.
type Config struct {
	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8000"`
	GRPCAddr string `env:"GRPC_ADDR" envDefault:":9000"`

	DatabaseDSN string `env:"DATABASE_DSN,required"`
	DBPoolSize  int32  `env:"DB_POOL_SIZE" envDefault:"10"`

	RedisAddr string `env:"REDIS_ADDR" envDefault:"localhost:6379"`

	MinIOEndpoint  string `env:"MINIO_ENDPOINT,required"`
	MinIOAccessKey string `env:"MINIO_ACCESS_KEY,required"`
	MinIOSecretKey string `env:"MINIO_SECRET_KEY,required"`
	MinIOBucket    string `env:"MINIO_BUCKET" envDefault:"crawler-artifacts"`
	MinIOSecure    bool   `env:"MINIO_SECURE" envDefault:"false"`

	JWTSecret string        `env:"JWT_SECRET,required"`
	JWTTTL    time.Duration `env:"JWT_TTL" envDefault:"24h"`

	WorkerSharedSecret string `env:"WORKER_SHARED_SECRET,required"`

	BcryptCost int `env:"BCRYPT_COST" envDefault:"10"`
}

func LoadConfig() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
