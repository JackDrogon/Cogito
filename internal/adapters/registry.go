package adapters

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Factory func() Adapter

type Registration struct {
	Name         string
	Capabilities CapabilityMatrix
	New          Factory
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Registration{}
)

func Register(reg Registration) error {
	name := strings.TrimSpace(reg.Name)
	if name == "" {
		return fmt.Errorf("adapters.Register: name is required")
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
