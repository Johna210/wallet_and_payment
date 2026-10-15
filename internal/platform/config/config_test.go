package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{"DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.DBMaxConns != 10 || cfg.LogLevel != slog.LevelInfo ||
		cfg.RequestTimeout != 10*time.Second || cfg.MaxBodyBytes != 1<<20 ||
		cfg.MaxOperationAmount != 100_000_000 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{
		"DATABASE_URL":               "postgres://x",
		"HTTP_ADDR":                  ":9090",
		"DB_MAX_CONNS":               "25",
		"LOG_LEVEL":                  "debug",
		"REQUEST_TIMEOUT":            "3s",
		"HTTP_WRITE_TIMEOUT":         "5s",
		"MAX_OPERATION_AMOUNT_MINOR": "5000",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":9090" || cfg.DBMaxConns != 25 || cfg.LogLevel != slog.LevelDebug ||
		cfg.RequestTimeout != 3*time.Second || cfg.MaxOperationAmount != 5000 {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := LoadFrom(env(map[string]string{
		"DB_MAX_CONNS":               "-1",
		"LOG_LEVEL":                  "loud",
		"REQUEST_TIMEOUT":            "soon",
		"MAX_OPERATION_AMOUNT_MINOR": "0",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"DATABASE_URL", "DB_MAX_CONNS", "LOG_LEVEL", "REQUEST_TIMEOUT", "MAX_OPERATION_AMOUNT_MINOR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestLoadRejectsWriteTimeoutNotAboveRequestTimeout(t *testing.T) {
	_, err := LoadFrom(env(map[string]string{
		"DATABASE_URL":       "postgres://x",
		"REQUEST_TIMEOUT":    "10s",
		"HTTP_WRITE_TIMEOUT": "10s",
	}))
	if err == nil || !strings.Contains(err.Error(), "HTTP_WRITE_TIMEOUT") {
		t.Fatalf("expected HTTP_WRITE_TIMEOUT error, got %v", err)
	}
}
