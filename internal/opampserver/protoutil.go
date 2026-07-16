package opampserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/opsramp/opamp-orchestrator/internal/model"
	"google.golang.org/protobuf/encoding/protojson"
)

// packageStatusesToMap renders reported package statuses as a generic JSON map
// for storage in the agents.package_statuses JSONB column.
func packageStatusesToMap(ps *protobufs.PackageStatuses) map[string]any {
	if ps == nil {
		return map[string]any{}
	}
	b, err := protojson.Marshal(ps)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// attrsToMap flattens a list of OpAMP KeyValue attributes to a string map.
func attrsToMap(kvs []*protobufs.KeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		if kv == nil {
			continue
		}
		out[kv.Key] = anyValueToString(kv.Value)
	}
	return out
}

func anyValueToString(v *protobufs.AnyValue) string {
	if v == nil {
		return ""
	}
	switch x := v.Value.(type) {
	case *protobufs.AnyValue_StringValue:
		return x.StringValue
	case *protobufs.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	case *protobufs.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *protobufs.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64)
	case *protobufs.AnyValue_BytesValue:
		return hex.EncodeToString(x.BytesValue)
	default:
		return fmt.Sprintf("%v", v.Value)
	}
}

// ConfigMapHash computes a canonical, order-independent sha256 over a config
// map. The same function is used when a config version is created and when an
// offer is built, so hashes always agree.
func ConfigMapHash(files map[string]model.ConfigFile) []byte {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		f := files[name]
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(f.ContentType))
		h.Write([]byte{0})
		h.Write([]byte(f.Body))
		h.Write([]byte{0})
	}
	return h.Sum(nil)
}

// ConfigMapHashHex is the hex-encoded form of ConfigMapHash.
func ConfigMapHashHex(files map[string]model.ConfigFile) string {
	return hex.EncodeToString(ConfigMapHash(files))
}

// effectiveConfigToString renders an agent's reported effective config map into
// a single human-readable string for storage/display.
func effectiveConfigToString(ec *protobufs.EffectiveConfig) string {
	if ec == nil || ec.ConfigMap == nil {
		return ""
	}
	names := make([]string, 0, len(ec.ConfigMap.ConfigMap))
	for name := range ec.ConfigMap.ConfigMap {
		names = append(names, name)
	}
	sort.Strings(names)
	out := ""
	for _, name := range names {
		f := ec.ConfigMap.ConfigMap[name]
		if len(names) > 1 || name != "" {
			out += "# --- " + name + " ---\n"
		}
		if f != nil {
			out += string(f.Body) + "\n"
		}
	}
	return out
}

// effectiveConfigHash derives a stable hex hash of the agent's effective config
// map so the UI can tell whether an agent's effective state matches a version.
func effectiveConfigHash(ec *protobufs.EffectiveConfig) string {
	if ec == nil || ec.ConfigMap == nil {
		return ""
	}
	files := make(map[string]model.ConfigFile, len(ec.ConfigMap.ConfigMap))
	for name, f := range ec.ConfigMap.ConfigMap {
		if f == nil {
			files[name] = model.ConfigFile{}
			continue
		}
		files[name] = model.ConfigFile{Body: string(f.Body), ContentType: f.ContentType}
	}
	return ConfigMapHashHex(files)
}

// modelFilesToConfigMap converts stored config files into an OpAMP config map.
func modelFilesToConfigMap(files map[string]model.ConfigFile) *protobufs.AgentConfigMap {
	cm := &protobufs.AgentConfigMap{ConfigMap: make(map[string]*protobufs.AgentConfigFile, len(files))}
	for name, f := range files {
		ct := f.ContentType
		if ct == "" {
			ct = "text/yaml"
		}
		cm.ConfigMap[name] = &protobufs.AgentConfigFile{Body: []byte(f.Body), ContentType: ct}
	}
	return cm
}
