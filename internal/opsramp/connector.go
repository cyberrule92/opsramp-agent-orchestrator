package opsramp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"
)

// ConnectorStore is the persistence the Connector needs. *store.Postgres
// satisfies it.
type ConnectorStore interface {
	GetOpsRampSettings(ctx context.Context) (*model.OpsRampSettings, error)
	SaveOpsRampSettings(ctx context.Context, s model.OpsRampSettings) error
	UpsertOpsRampAgent(ctx context.Context, a model.OpsRampAgent) error
	ListOpsRampAgents(ctx context.Context) ([]model.OpsRampAgent, error)
}

// Connector manages the OpsRamp client and inventory poller, and supports live
// reconfiguration from the UI. It is safe for concurrent use.
type Connector struct {
	store ConnectorStore
	log   *slog.Logger

	mu       sync.RWMutex
	settings model.OpsRampSettings
	enabled  bool
	client   *Client

	rootCtx    context.Context
	pollCancel context.CancelFunc
	wg         sync.WaitGroup
}

// NewConnector builds a Connector. Call Start to load config and begin polling.
func NewConnector(store ConnectorStore, log *slog.Logger) *Connector {
	return &Connector{store: store, log: log}
}

// Start loads persisted settings (seeding from envDefault on first run when the
// DB has none and envDefault is complete), then begins polling if enabled.
func (c *Connector) Start(ctx context.Context, envDefault model.OpsRampSettings) error {
	c.rootCtx = ctx

	persisted, err := c.store.GetOpsRampSettings(ctx)
	if err != nil {
		return fmt.Errorf("load opsramp settings: %w", err)
	}

	var s model.OpsRampSettings
	switch {
	case persisted != nil:
		s = *persisted
	case envDefault.Complete():
		// First boot with env credentials: persist them as the initial config.
		envDefault.Enabled = true
		if envDefault.PollIntervalSeconds <= 0 {
			envDefault.PollIntervalSeconds = 60
		}
		if err := c.store.SaveOpsRampSettings(ctx, envDefault); err != nil {
			c.log.Warn("persist env opsramp settings", "err", err)
		}
		s = envDefault
	default:
		c.log.Info("opsramp connector disabled (not configured)")
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.apply(s)
	if c.enabled {
		c.log.Info("opsramp connector enabled", "api", s.BaseURL, "tenant", s.TenantID,
			"poll_interval", c.interval().String())
	} else {
		c.log.Info("opsramp connector configured but disabled")
	}
	return nil
}

// apply swaps in the given settings and (re)starts the poll loop. Caller holds mu.
func (c *Connector) apply(s model.OpsRampSettings) {
	if c.pollCancel != nil {
		c.pollCancel()
		c.pollCancel = nil
	}
	c.settings = s
	c.enabled = s.Enabled && s.Complete()
	if !c.enabled {
		c.client = nil
		return
	}
	c.client = New(Config{
		BaseURL:      s.BaseURL,
		TenantID:     s.TenantID,
		ClientKey:    s.ClientKey,
		ClientSecret: s.ClientSecret,
	})
	if c.rootCtx != nil {
		ctx, cancel := context.WithCancel(c.rootCtx)
		c.pollCancel = cancel
		client := c.client
		interval := c.interval()
		c.wg.Add(1)
		go c.pollLoop(ctx, client, interval)
	}
}

func (c *Connector) interval() time.Duration {
	sec := c.settings.PollIntervalSeconds
	if sec < 30 {
		sec = 60
	}
	return time.Duration(sec) * time.Second
}

func (c *Connector) pollLoop(ctx context.Context, client *Client, interval time.Duration) {
	defer c.wg.Done()
	if n, err := c.syncWith(ctx, client); err != nil {
		c.log.Warn("opsramp initial sync failed", "err", err)
	} else {
		c.log.Info("opsramp initial sync complete", "agents", n)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := c.syncWith(ctx, client); err != nil {
				c.log.Warn("opsramp sync failed", "err", err)
			} else {
				c.log.Debug("opsramp sync complete", "agents", n)
			}
		}
	}
}

func (c *Connector) syncWith(ctx context.Context, client *Client) (int, error) {
	resources, err := client.ListAgentResources(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range resources {
		if err := c.store.UpsertOpsRampAgent(ctx, toModel(r)); err != nil {
			c.log.Warn("upsert opsramp agent", "resource_id", r.ID, "err", err)
			continue
		}
		n++
	}
	return n, nil
}

// Reconfigure validates and persists new settings, then swaps the live client
// and poller. A missing ClientSecret keeps the previously stored secret.
func (c *Connector) Reconfigure(ctx context.Context, in model.OpsRampSettings) error {
	c.mu.Lock()
	prev := c.settings
	c.mu.Unlock()

	if in.ClientSecret == "" {
		in.ClientSecret = prev.ClientSecret
	}
	if in.PollIntervalSeconds <= 0 {
		in.PollIntervalSeconds = 60
	}
	if !in.Complete() {
		in.Enabled = false
	}

	// Validate credentials before saving when enabling.
	if in.Enabled {
		test := New(Config{BaseURL: in.BaseURL, TenantID: in.TenantID, ClientKey: in.ClientKey, ClientSecret: in.ClientSecret})
		if err := test.Ping(ctx); err != nil {
			return fmt.Errorf("credential check failed: %w", err)
		}
	}

	if err := c.store.SaveOpsRampSettings(ctx, in); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	c.mu.Lock()
	c.apply(in)
	c.mu.Unlock()
	c.log.Info("opsramp connector reconfigured", "api", in.BaseURL, "tenant", in.TenantID, "enabled", c.IsEnabled())
	return nil
}

// CurrentClient returns the active client, or (nil, false) when disabled.
func (c *Connector) CurrentClient() (*Client, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.enabled || c.client == nil {
		return nil, false
	}
	return c.client, true
}

// IsEnabled reports whether the connector is active.
func (c *Connector) IsEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.enabled
}

// SyncNow triggers an immediate inventory sync with the active client.
func (c *Connector) SyncNow(ctx context.Context) (int, error) {
	client, ok := c.CurrentClient()
	if !ok {
		return 0, fmt.Errorf("opsramp connector is not enabled")
	}
	return c.syncWith(ctx, client)
}

// Settings returns a copy of the current settings with the secret cleared.
func (c *Connector) Settings() model.OpsRampSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.settings
	s.ClientSecret = ""
	return s
}

// HasSecret reports whether a client secret is currently stored.
func (c *Connector) HasSecret() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings.ClientSecret != ""
}

// DeployScript returns the OpsRamp deployAgent.sh installer contents.
func (c *Connector) DeployScript(ctx context.Context) ([]byte, error) {
	client, ok := c.CurrentClient()
	if !ok {
		return nil, fmt.Errorf("opsramp connector is not enabled")
	}
	return client.GetDeployScript(ctx)
}

// InstallParams returns the API host (without scheme), client key and secret for
// running the agent installer. enabled is false when the connector is disabled.
func (c *Connector) InstallParams() (apiHost, key, secret string, enabled bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.enabled {
		return "", "", "", false
	}
	host := c.settings.BaseURL
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimRight(host, "/")
	return host, c.settings.ClientKey, c.settings.ClientSecret, true
}

// DeregisterByHost finds the OpsRamp resource whose IP or hostname matches host
// and deletes it from the tenant. found is false when nothing matched.
func (c *Connector) DeregisterByHost(ctx context.Context, host string) (bool, error) {
	client, ok := c.CurrentClient()
	if !ok {
		return false, fmt.Errorf("opsramp connector is not enabled")
	}
	agents, err := c.store.ListOpsRampAgents(ctx)
	if err != nil {
		return false, err
	}
	for _, a := range agents {
		if a.IPAddress == host || a.HostName == host {
			if err := client.DeleteResource(ctx, a.ResourceID); err != nil {
				return true, err
			}
			return true, nil
		}
	}
	return false, nil
}

// Ping checks credentials of the active client.
func (c *Connector) Ping(ctx context.Context) error {
	client, ok := c.CurrentClient()
	if !ok {
		return fmt.Errorf("opsramp connector is not enabled")
	}
	return client.Ping(ctx)
}
