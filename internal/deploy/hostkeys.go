package deploy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
)

// KnownHosts implements trust-on-first-use (TOFU) host-key verification: a
// host's key is recorded on first contact and pinned thereafter. A later key
// that differs from the pinned one is rejected as a possible MITM. This gives
// meaningful protection for bulk provisioning without requiring pre-seeded keys,
// and never silently trusts a changed key.
type KnownHosts struct {
	path string
	mu   sync.Mutex
	keys map[string]string // host -> base64(marshaled public key)
}

// NewKnownHosts loads (or initializes) a known-hosts store at path.
func NewKnownHosts(path string) (*KnownHosts, error) {
	kh := &KnownHosts{path: path, keys: map[string]string{}}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &kh.keys)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return kh, nil
}

// Callback returns an ssh.HostKeyCallback enforcing the TOFU policy.
func (k *KnownHosts) Callback() ssh.HostKeyCallback {
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		host := hostname
		if h, _, err := net.SplitHostPort(hostname); err == nil {
			host = h
		}
		fp := base64.StdEncoding.EncodeToString(key.Marshal())

		k.mu.Lock()
		defer k.mu.Unlock()
		if existing, ok := k.keys[host]; ok {
			if existing != fp {
				return fmt.Errorf("host key mismatch for %s (possible MITM); "+
					"remove it from the known-hosts store to re-pin", host)
			}
			return nil
		}
		k.keys[host] = fp
		k.persistLocked()
		return nil
	}
}

func (k *KnownHosts) persistLocked() {
	if k.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(k.path), 0o755)
	data, err := json.MarshalIndent(k.keys, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(k.path, data, 0o600)
}
