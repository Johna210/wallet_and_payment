// Package config loads and validates the service configuration from
// environment variables. Every variable is documented in .env.example.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the complete runtime configuration of the service.
type Config struct {
	HTTPAddr         string
	DatabaseURL      string
	DBMaxConns       int32
	LogLevel         slog.Level
	HTTPReadTimeout  time.Duration
	HTTPWriteTimeout time.Duration
	HTTPIdleTimeout  time.Duration
	RequestTimeout   time.Duration
	MaxBodyBytes     int64
	ShutdownDelay    time.Duration
	ShutdownTimeout  time.Duration
}

// Load reads the configuration from the process environment.
func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

// LoadFrom reads the configuration through lookup, which has the signature of
// os.LookupEnv. All problems are reported together rather than one at a time.
func LoadFrom(lookup func(string) (string, bool)) (Config, error) {
	p := parser{lookup: lookup}
	cfg := Config{
		HTTPAddr:         p.str("HTTP_ADDR", ":8080"),
		DatabaseURL:      p.required("DATABASE_URL"),
		DBMaxConns:       int32(p.positiveInt("DB_MAX_CONNS", 10)),
		LogLevel:         p.logLevel("LOG_LEVEL", slog.LevelInfo),
		HTTPReadTimeout:  p.duration("HTTP_READ_TIMEOUT", 10*time.Second),
		HTTPWriteTimeout: p.duration("HTTP_WRITE_TIMEOUT", 15*time.Second),
		HTTPIdleTimeout:  p.duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		RequestTimeout:   p.duration("REQUEST_TIMEOUT", 10*time.Second),
		MaxBodyBytes:     int64(p.positiveInt("MAX_BODY_BYTES", 1<<20)),
		ShutdownDelay:    p.duration("SHUTDOWN_DELAY", 5*time.Second),
		ShutdownTimeout:  p.duration("SHUTDOWN_TIMEOUT", 20*time.Second),
	}

	if cfg.HTTPWriteTimeout <= cfg.RequestTimeout {
		p.fail("HTTP_WRITE_TIMEOUT must be greater than REQUEST_TIMEOUT so timed-out requests can still write their error")
	}
	if cfg.DBMaxConns > 1000 {
		p.fail("DB_MAX_CONNS must be at most 1000")
	}

	if len(p.errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(p.errs...))
	}
	return cfg, nil
}

type parser struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (p *parser) fail(format string, args ...any) {
	p.errs = append(p.errs, fmt.Errorf(format, args...))
}

func (p *parser) value(key string) (string, bool) {
	v, ok := p.lookup(key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

func (p *parser) str(key, def string) string {
	if v, ok := p.value(key); ok {
		return v
	}
	return def
}

func (p *parser) required(key string) string {
	v, ok := p.value(key)
	if !ok {
		p.fail("%s is required", key)
	}
	return v
}

func (p *parser) positiveInt(key string, def int) int {
	v, ok := p.value(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		p.fail("%s must be a positive integer, got %q", key, v)
		return def
	}
	return n
}

func (p *parser) duration(key string, def time.Duration) time.Duration {
	v, ok := p.value(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		p.fail("%s must be a positive duration such as 10s, got %q", key, v)
		return def
	}
	return d
}

func (p *parser) logLevel(key string, def slog.Level) slog.Level {
	v, ok := p.value(key)
	if !ok {
		return def
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		p.fail("%s must be one of debug, info, warn, error, got %q", key, v)
		return def
	}
	return l
}
