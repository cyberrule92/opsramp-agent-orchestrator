package opampserver

import (
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/opsramp/opamp-orchestrator/internal/store"
)

// storeDescription assembles the persisted AgentDescription, extracting a few
// well-known attributes for first-class columns.
func storeDescription(ident, nonident map[string]string, caps uint64) store.AgentDescription {
	pick := func(key string) string {
		if v, ok := ident[key]; ok {
			return v
		}
		return nonident[key]
	}
	return store.AgentDescription{
		ServiceName:         pick("service.name"),
		ServiceVersion:      pick("service.version"),
		Hostname:            pick("host.name"),
		OSType:              pick("os.type"),
		Capabilities:        caps,
		IdentifyingAttrs:    ident,
		NonIdentifyingAttrs: nonident,
	}
}

func healthFromProto(h *protobufs.ComponentHealth) store.AgentHealth {
	return store.AgentHealth{
		Healthy:    h.Healthy,
		Status:     h.Status,
		LastError:  h.LastError,
		StartNano:  h.StartTimeUnixNano,
		StatusNano: h.StatusTimeUnixNano,
	}
}
