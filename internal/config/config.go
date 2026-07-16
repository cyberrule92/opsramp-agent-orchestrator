// Package config loads orchestrator configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for the orchestrator.
type Config struct {
	// OpAMP server (agent-facing) settings.
	OpAMPListen string // e.g. ":4320"
	OpAMPPath   string // e.g. "/v1/opamp"
	AuthToken   string // optional bearer token required from agents

	// Admin API + UI (operator-facing) settings.
	AdminListen string // e.g. ":8080"
	AdminToken  string // optional bearer token required for mutating admin API calls

	// PublicBaseURL is the externally reachable base URL of the admin API. It is
	// used to build package download URLs handed to agents.
	PublicBaseURL string

	// DatabaseURL is a standard libpq/pgx connection string.
	DatabaseURL string

	// DefaultGroup is the group assigned to agents that match nothing else.
	DefaultGroup string

	LogLevel string

	// ShutdownTimeout bounds graceful shutdown.
	ShutdownTimeout time.Duration

	// OpsRamp connector (optional). When creds are present the orchestrator
	// polls the OpsRamp Resources API to monitor OpsRamp-managed agents.
	OpsRamp OpsRampConfig

	// Deployment (bulk agent install over SSH).
	DeployStateDir    string // holds the TOFU known-hosts store
	DeployConcurrency int
}

// OpsRampConfig holds OpsRamp REST API connection settings.
type OpsRampConfig struct {
	BaseURL      string
	TenantID     string
	ClientKey    string
	ClientSecret string
	PollInterval time.Duration
}

// Enabled reports whether the OpsRamp connector has enough config to run.
func (o OpsRampConfig) Enabled() bool {
	return o.BaseURL != "" && o.TenantID != "" && o.ClientKey != "" && o.ClientSecret != ""
}

// Load reads configuration from environment variables, applying sane defaults.
func Load() (*Config, error) {
	c := &Config{
		OpAMPListen:     env("OPAMP_LISTEN_ENDPOINT", ":4320"),
		OpAMPPath:       env("OPAMP_LISTEN_PATH", "/v1/opamp"),
		AuthToken:       env("OPAMP_AUTH_TOKEN", ""),
		AdminListen:     env("ADMIN_LISTEN_ENDPOINT", ":8080"),
		AdminToken:      env("ADMIN_AUTH_TOKEN", ""),
		PublicBaseURL:   strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		DatabaseURL:     env("DATABASE_URL", ""),
		DefaultGroup:    env("DEFAULT_GROUP", "default"),
		LogLevel:        env("LOG_LEVEL", "info"),
		ShutdownTimeout: 15 * time.Second,
		OpsRamp: OpsRampConfig{
			BaseURL:      strings.TrimRight(env("OPSRAMP_API_URL", ""), "/"),
			TenantID:     env("OPSRAMP_TENANT_ID", ""),
			ClientKey:    env("OPSRAMP_CLIENT_KEY", ""),
			ClientSecret: env("OPSRAMP_CLIENT_SECRET", ""),
			PollInterval: envDuration("OPSRAMP_POLL_INTERVAL", 60*time.Second),
		},
		DeployStateDir:    env("DEPLOY_STATE_DIR", "/var/lib/orchestrator"),
		DeployConcurrency: envInt("DEPLOY_CONCURRENCY", 10),
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	return c, nil
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
