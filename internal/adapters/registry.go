package adapters

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

type Factory func() Adapter

// AdapterOptions carries runtime-resolved configuration into adapter
// construction without exposing provider-specific Config types to callers.
// Fields that a given provider does not understand are ignored.
type AdapterOptions struct {
	// Sandbox selects the codex sandbox mode (e.g. "danger-full-access").
	// Providers without a sandbox concept ignore it.
	Sandbox string

	// Model overrides the provider model. Empty means provider default.
	Model string

	// LogDir is the per-step directory that receives provider process logs.
	// Empty disables log-file capture.
	LogDir string

	// LiveSink, when non-nil, receives the provider's stdout+stderr stream in
	// real time (AgentLoop-style live output, e.g. for `-v`). The writer must
	// be safe for concurrent use; nil disables live streaming.
	LiveSink io.Writer
}

// OptionFactory builds an adapter from runtime-resolved options. Providers
// register one alongside New so app wiring can inject configuration without
// importing provider subpackages.
type OptionFactory func(AdapterOptions) Adapter

type Registration struct {
	Name         string
	Capabilities CapabilityMatrix
	New          Factory
	// NewWithOptions is an optional configuration-aware factory. When nil,
	// Build falls back to New.
	NewWithOptions OptionFactory
}

// Build constructs an adapter, preferring the option-aware factory when the
// registration provides one. It lets callers inject AdapterOptions uniformly
// regardless of whether a provider opted into configuration.
func (r Registration) Build(options AdapterOptions) Adapter {
	if r.NewWithOptions != nil {
		return r.NewWithOptions(options)
	}

	return r.New()
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Registration{}
)

func Register(reg Registration) error {
	name := strings.TrimSpace(reg.Name)
	if name == "" {
		return errors.New("adapters.Register: name is required")
	}

	if reg.New == nil {
		return fmt.Errorf("adapter registration factory is required for %q", name)
	}

	registryMu.Lock()
	defer registryMu.Unlock()

	if _, exists := registry[name]; exists {
		return fmt.Errorf("adapter registration already exists for %q", name)
	}

	reg.Name = name
	registry[name] = reg

	return nil
}

func Lookup(name string) (Registration, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	reg, ok := registry[strings.TrimSpace(name)]
	if !ok {
		return Registration{}, false
	}

	return reg, true
}

func RegisteredNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}
