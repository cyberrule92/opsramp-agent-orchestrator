// Package store defines the persistence layer for the orchestrator and its
// Postgres-backed implementation.
package store

import (
	"context"

	"github.com/opsramp/opamp-orchestrator/internal/model"
)

// Store is the persistence contract used by the OpAMP server and admin API.
type Store interface {
	// --- Agent state (written by the OpAMP server on each message) ---

	// TouchAgent upserts the agent row, marking it seen/connected now.
	TouchAgent(ctx context.Context, uid string) error
	SetAgentStatus(ctx context.Context, uid, status string) error
	SaveAgentDescription(ctx context.Context, uid string, d AgentDescription) error
	SaveAgentHealth(ctx context.Context, uid string, h AgentHealth) error
	SaveAgentEffectiveConfig(ctx context.Context, uid, cfg, hash string) error
	SaveAgentRemoteConfigStatus(ctx context.Context, uid string, status int32, errMsg, lastHashHex string) error
	SaveAgentPackageStatuses(ctx context.Context, uid string, statuses map[string]any) error
	SetAgentGroup(ctx context.Context, uid string, group *string) error

	GetAgent(ctx context.Context, uid string) (*model.Agent, error)
	ListAgents(ctx context.Context) ([]model.Agent, error)

	// --- Groups & versioned configs ---

	ListGroups(ctx context.Context) ([]model.Group, error)
	GetGroup(ctx context.Context, name string) (*model.Group, error)
	UpsertGroup(ctx context.Context, g model.Group) error
	DeleteGroup(ctx context.Context, name string) error

	// CreateConfigVersion appends a new version to the group and makes it current.
	CreateConfigVersion(ctx context.Context, group string, files map[string]model.ConfigFile, hash, note, createdBy string) (*model.ConfigVersion, error)
	SetCurrentConfigVersion(ctx context.Context, group string, version int) error
	GetCurrentConfig(ctx context.Context, group string) (*model.ConfigVersion, error)
	ListConfigVersions(ctx context.Context, group string) ([]model.ConfigVersion, error)

	// --- Packages ---

	UpsertPackage(ctx context.Context, p model.Package, content []byte) error
	GetPackage(ctx context.Context, name string) (*model.Package, error)
	GetPackageContent(ctx context.Context, name string) ([]byte, error)
	ListPackages(ctx context.Context) ([]model.Package, error)
	DeletePackage(ctx context.Context, name string) error
	AssignPackage(ctx context.Context, group, pkg string) error
	UnassignPackage(ctx context.Context, group, pkg string) error
	ListGroupPackages(ctx context.Context, group string) ([]model.Package, error)

	// --- Events ---

	AddEvent(ctx context.Context, uid, kind, detail string) error
	ListEvents(ctx context.Context, limit int) ([]model.Event, error)

	// --- OpsRamp agent inventory ---

	UpsertOpsRampAgent(ctx context.Context, a model.OpsRampAgent) error
	ListOpsRampAgents(ctx context.Context) ([]model.OpsRampAgent, error)
	CountOpsRampAgents(ctx context.Context) (int, error)

	// GetOpsRampSettings returns the persisted connector config, or (nil, nil)
	// when it has never been set.
	GetOpsRampSettings(ctx context.Context) (*model.OpsRampSettings, error)
	SaveOpsRampSettings(ctx context.Context, s model.OpsRampSettings) error

	// --- Deployment jobs ---

	CreateDeployJob(ctx context.Context, job model.DeployJob, hosts []string) error
	SetDeployJobStatus(ctx context.Context, id, status string, succeeded, failed int, finished bool) error
	UpsertDeployHostResult(ctx context.Context, r model.DeployHostResult) error
	ListDeployJobs(ctx context.Context, limit int) ([]model.DeployJob, error)
	GetDeployJob(ctx context.Context, id string) (*model.DeployJob, error)

	Close()
}

// AgentDescription is the subset of AgentDescription persisted per agent.
type AgentDescription struct {
	ServiceName         string
	ServiceVersion      string
	Hostname            string
	OSType              string
	Capabilities        uint64
	IdentifyingAttrs    map[string]string
	NonIdentifyingAttrs map[string]string
}

// AgentHealth is the subset of ComponentHealth persisted per agent.
type AgentHealth struct {
	Healthy   bool
	Status    string
	LastError string
	StartNano uint64
	StatusNano uint64
}
