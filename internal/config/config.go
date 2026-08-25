package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Address         string
	DatabasePath    string
	SessionTTL      time.Duration
	WorkerInterval  time.Duration
	ShutdownTimeout time.Duration
	MaxRequestBytes int64
}

func Load() (Config, error) {
	cfg := Config{
		Address:         env("VESSELTRIAL_ADDR", ":8080"),
		DatabasePath:    env("VESSELTRIAL_DB", "vesseltrial.db"),
		SessionTTL:      8 * time.Hour,
		WorkerInterval:  5 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		MaxRequestBytes: 1 << 20,
	}
	var err error
	if cfg.SessionTTL, err = duration("VESSELTRIAL_SESSION_TTL", cfg.SessionTTL); err != nil {
		return Config{}, err
	}
	if cfg.WorkerInterval, err = duration("VESSELTRIAL_WORKER_INTERVAL", cfg.WorkerInterval); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = duration("VESSELTRIAL_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if raw := os.Getenv("VESSELTRIAL_MAX_REQUEST_BYTES"); raw != "" {
		cfg.MaxRequestBytes, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cfg.MaxRequestBytes < 1024 {
			return Config{}, fmt.Errorf("VESSELTRIAL_MAX_REQUEST_BYTES: invalid positive size")
		}
	}
	if cfg.DatabasePath == "" || cfg.Address == "" {
		return Config{}, fmt.Errorf("address and database path are required")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func duration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s: invalid positive duration", key)
	}
	return value, nil
}
