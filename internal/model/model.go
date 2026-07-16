// Package model defines the orchestrator's domain types shared across the
// store, OpAMP server, and admin API layers.
package model

import "time"

// ConfigFile is a single named configuration file offered to agents.
type ConfigFile struct {
	Body        string `json:"body"`
	ContentType string `json:"content_type"`
}

// Group is a logical set of agents that share a desired configuration and
// package set. Agents are matched to a group by explicit assignment or by the
// group's label Selector against agent attributes.
type Group struct {
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Selector       map[string]string `json:"selector"`
	CurrentVersion int               `json:"current_version"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// ConfigVersion is an immutable, versioned snapshot of a group's config map.
type ConfigVersion struct {
	ID        int64                 `json:"id"`
	GroupName string                `json:"group_name"`
	Version   int                   `json:"version"`
	Files     map[string]ConfigFile `json:"files"`
	Hash      string                `json:"hash"` // hex sha256 over the canonical config map
	Note      string                `json:"note"`
	CreatedBy string                `json:"created_by"`
	CreatedAt time.Time             `json:"created_at"`
}

// Package describes a downloadable artifact (agent binary or addon) that the
// orchestrator can offer to agents via OpAMP package management.
type Package struct {
	Name        string    `json:"name"`
	Type        int32     `json:"type"` // 0 = top-level, 1 = addon
	Version     string    `json:"version"`
	ContentHash string    `json:"content_hash"` // hex sha256 of Content
	Signature   string    `json:"signature"`
	Size        int64     `json:"size"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Agent is the last-known state of a managed monitoring agent.
type Agent struct {
	InstanceUID         string            `json:"instance_uid"`
	Status              string            `json:"status"` // connected | disconnected
	ServiceName         string            `json:"service_name"`
	ServiceVersion      string            `json:"service_version"`
	Hostname            string            `json:"hostname"`
	OSType              string            `json:"os_type"`
	IdentifyingAttrs    map[string]string `json:"identifying_attrs"`
	NonIdentifyingAttrs map[string]string `json:"non_identifying_attrs"`
	Capabilities        uint64            `json:"capabilities"`
	EffectiveConfig     string            `json:"effective_config"`
	EffectiveConfigHash string            `json:"effective_config_hash"`
	RemoteConfigStatus  int32             `json:"remote_config_status"` // 0 UNSET,1 APPLIED,2 APPLYING,3 FAILED
	RemoteConfigError   string            `json:"remote_config_error"`
	LastRemoteConfigHash string           `json:"last_remote_config_hash"` // hex
	HealthHealthy       bool              `json:"health_healthy"`
	HealthStatus        string            `json:"health_status"`
	HealthLastError     string            `json:"health_last_error"`
	HealthStartTime     *time.Time        `json:"health_start_time,omitempty"`
	HealthStatusTime    *time.Time        `json:"health_status_time,omitempty"`
	PackageStatuses     map[string]any    `json:"package_statuses"`
	AssignedGroup       *string           `json:"assigned_group,omitempty"`
	ResolvedGroup       string            `json:"resolved_group"` // computed, not stored
	FirstSeen           time.Time         `json:"first_seen"`
	LastSeen            time.Time         `json:"last_seen"`
}

// OpsRampAgent is an OpsRamp-managed agent, synced from the OpsRamp Resources
// Search API (agentInstalled resources). OpsRamp agents are managed via the
// OpsRamp REST API rather than OpAMP, so they are tracked separately.
type OpsRampAgent struct {
	ResourceID     string         `json:"resource_id"`
	Name           string         `json:"name"`
	HostName       string         `json:"host_name"`
	IPAddress      string         `json:"ip_address"`
	ResourceType   string         `json:"resource_type"`
	AgentInstalled bool           `json:"agent_installed"`
	AgentVersion   string         `json:"agent_version"`
	AgentStatus    string         `json:"agent_status"`
	ClientID       string         `json:"client_id"`
	Raw            map[string]any `json:"raw,omitempty"`
	FirstSeen      time.Time      `json:"first_seen"`
	LastSynced     time.Time      `json:"last_synced"`
}

// OpsRampSettings is the runtime-editable OpsRamp connector configuration,
// persisted so it can be changed from the UI without a restart.
type OpsRampSettings struct {
	BaseURL             string    `json:"base_url"`
	TenantID            string    `json:"tenant_id"`
	ClientKey           string    `json:"client_key"`
	ClientSecret        string    `json:"client_secret"`
	PollIntervalSeconds int       `json:"poll_interval_seconds"`
	Enabled             bool      `json:"enabled"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Complete reports whether all fields required to connect are present.
func (s OpsRampSettings) Complete() bool {
	return s.BaseURL != "" && s.TenantID != "" && s.ClientKey != "" && s.ClientSecret != ""
}

// DeployJob is a bulk agent-installation run across a set of VMs. SSH
// credentials are intentionally NOT part of this record — they live only in
// memory for the duration of the run and are never persisted.
type DeployJob struct {
	ID            string             `json:"id"`
	Action        string             `json:"action"` // install|preflight|repair|upgrade|uninstall
	Status        string             `json:"status"` // pending|running|succeeded|failed|partial
	TargetSpec    string             `json:"target_spec"`
	SSHUser       string             `json:"ssh_user"`
	Port          int                `json:"port"`
	UseSudo       bool               `json:"use_sudo"`
	IntegrationID string             `json:"integration_id"`
	Total         int                `json:"total"`
	Succeeded     int                `json:"succeeded"`
	Failed        int                `json:"failed"`
	CreatedBy     string             `json:"created_by"`
	CreatedAt     time.Time          `json:"created_at"`
	FinishedAt    *time.Time         `json:"finished_at,omitempty"`
	Hosts         []DeployHostResult `json:"hosts,omitempty"`
}

// DeployHostResult is the per-host outcome of a deployment.
type DeployHostResult struct {
	JobID      string    `json:"job_id"`
	Host       string    `json:"host"`
	Status     string    `json:"status"` // pending|running|success|failed
	ExitCode   int       `json:"exit_code"`
	Output     string    `json:"output"`
	Error      string    `json:"error"`
	DurationMs int64     `json:"duration_ms"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Event is an audit/observability record for an agent lifecycle change.
type Event struct {
	ID          int64     `json:"id"`
	InstanceUID string    `json:"instance_uid"`
	Kind        string    `json:"kind"`
	Detail      string    `json:"detail"`
	TS          time.Time `json:"ts"`
}
