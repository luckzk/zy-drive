// SPDX-License-Identifier: MPL-2.0
package storage

import (
	"fmt"
	"sort"
	"sync"

	"github.com/luckzk/zy-drive/internal/domain"
)

// Factory builds a Provider from an account Config.
type Factory func(cfg Config) (Provider, error)

// Registry is the single assembly point for drivers. Adding a new cloud
// means: one new driver directory + one Register call here + one settings
// page. Sync, transfer, wizard and Agent protocol do not change.
type Registry struct {
	mu        sync.RWMutex
	factories map[domain.ProviderKind]Factory
}

var defaultRegistry = &Registry{factories: map[domain.ProviderKind]Factory{}}

// Default returns the process-wide registry.
func Default() *Registry { return defaultRegistry }

// Register adds a driver factory. Called once per kind, in cmd/* at
// startup. Registering the same kind twice is a programmer error.
func (r *Registry) Register(kind domain.ProviderKind, f Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.factories[kind]; ok {
		panic(fmt.Sprintf("storage: provider %q already registered", kind))
	}
	r.factories[kind] = f
}

// New builds a provider of the given kind. Unknown kinds are an error,
// never a silent fallback.
func (r *Registry) New(kind domain.ProviderKind, cfg Config) (Provider, error) {
	r.mu.RLock()
	f, ok := r.factories[kind]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("storage: provider %q not registered", kind)
	}
	return f(cfg)
}

// Kinds returns the registered provider kinds in stable order.
func (r *Registry) Kinds() []domain.ProviderKind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.ProviderKind, 0, len(r.factories))
	for k := range r.factories {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
