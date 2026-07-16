package opsramp

import (
	"encoding/json"

	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"
)

// toModel maps an OpsRamp resource to the stored agent inventory record.
func toModel(r Resource) model.OpsRampAgent {
	status := r.Status
	if status == "" {
		status = r.State
	}
	name := r.Name
	if name == "" {
		name = r.ResourceName
	}
	var raw map[string]any
	if len(r.Raw) > 0 {
		_ = json.Unmarshal(r.Raw, &raw)
	}
	return model.OpsRampAgent{
		ResourceID:     r.ID,
		Name:           name,
		HostName:       r.HostName,
		IPAddress:      r.IPAddress,
		ResourceType:   r.ResourceType,
		AgentInstalled: r.AgentInstalled,
		AgentVersion:   r.AgentVersion,
		AgentStatus:    status,
		ClientID:       r.ClientUniqueID,
		Raw:            raw,
	}
}
