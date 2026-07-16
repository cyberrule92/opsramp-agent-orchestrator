package main

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/proto"
)

// filePackagesProvider is a compact, filesystem-backed implementation of the
// opamp-go PackagesStateProvider. It lets the built-in package syncer download
// and track packages offered by the orchestrator. Package content is written
// under <dir>/packages/<name>; metadata is kept in memory (sufficient for the
// demo agent's lifetime and reconstructed from disk on demand).
type filePackagesProvider struct {
	dir string

	mu             sync.Mutex
	allHash        []byte
	states         map[string]types.PackageState
	lastReported   *protobufs.PackageStatuses
}

func newFilePackagesProvider(dir string) (*filePackagesProvider, error) {
	if err := os.MkdirAll(filepath.Join(dir, "packages"), 0o755); err != nil {
		return nil, err
	}
	return &filePackagesProvider{dir: dir, states: map[string]types.PackageState{}}, nil
}

func (p *filePackagesProvider) contentPath(name string) string {
	return filepath.Join(p.dir, "packages", name)
}

func (p *filePackagesProvider) AllPackagesHash() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allHash, nil
}

func (p *filePackagesProvider) SetAllPackagesHash(hash []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allHash = hash
	return nil
}

func (p *filePackagesProvider) Packages() ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.states))
	for name := range p.states {
		out = append(out, name)
	}
	return out, nil
}

func (p *filePackagesProvider) PackageState(name string) (types.PackageState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.states[name], nil
}

func (p *filePackagesProvider) SetPackageState(name string, state types.PackageState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.states[name] = state
	return nil
}

func (p *filePackagesProvider) CreatePackage(name string, typ protobufs.PackageType) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.states[name]; ok {
		return nil
	}
	p.states[name] = types.PackageState{Exists: true, Type: typ}
	return nil
}

func (p *filePackagesProvider) FileContentHash(name string) ([]byte, error) {
	data, err := os.ReadFile(p.contentPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sum := sha256.Sum256(data)
	return sum[:], nil
}

func (p *filePackagesProvider) UpdateContent(ctx context.Context, name string, data io.Reader, contentHash, signature []byte) error {
	f, err := os.Create(p.contentPath(name))
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, data); err != nil {
		return err
	}
	return nil
}

func (p *filePackagesProvider) DeletePackage(name string) error {
	p.mu.Lock()
	delete(p.states, name)
	p.mu.Unlock()
	err := os.Remove(p.contentPath(name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (p *filePackagesProvider) LastReportedStatuses() (*protobufs.PackageStatuses, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastReported == nil {
		return nil, nil
	}
	return proto.Clone(p.lastReported).(*protobufs.PackageStatuses), nil
}

func (p *filePackagesProvider) SetLastReportedStatuses(statuses *protobufs.PackageStatuses) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastReported = statuses
	return nil
}
