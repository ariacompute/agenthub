package sandbox

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// RegistryOptions configures the catalogue.
type RegistryOptions struct {
	// Default is the provider name the instance falls back to
	// (AGENTHUB_SANDBOX_PROVIDER). It is reported in the catalogue so the
	// console can mark it.
	Default string
	// ProbeTTL bounds how long a probe result is reused. Probes run a binary, so
	// they must never sit on a request path more often than this.
	ProbeTTL time.Duration
	// ProbeTimeout bounds one probe.
	ProbeTimeout time.Duration
	Clock        Clock
}

// Registry is the provider catalogue. It is the only place that knows which
// backends exist, which is what makes "add a backend without touching Go code"
// true: a declarative provider is registered here exactly like a built-in one.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	order     []string
	opts      RegistryOptions

	probeMu    sync.Mutex
	probeCache map[string]probeEntry
}

type probeEntry struct {
	info ProviderInfo
	at   time.Time
}

// NewRegistry builds an empty catalogue.
func NewRegistry(opts RegistryOptions) *Registry {
	if opts.ProbeTTL <= 0 {
		opts.ProbeTTL = 30 * time.Second
	}
	if opts.ProbeTimeout <= 0 {
		opts.ProbeTimeout = 2 * time.Second
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	return &Registry{
		providers:  map[string]Provider{},
		opts:       opts,
		probeCache: map[string]probeEntry{},
	}
}

// Register adds a provider. A duplicate name is refused rather than silently
// replaced: two definitions of "docker" would make the catalogue lie.
func (r *Registry) Register(p Provider) error {
	if p == nil || strings.TrimSpace(p.Name()) == "" {
		return Error(CodeUnknownProvider)
	}
	name := strings.TrimSpace(p.Name())
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[name]; exists {
		return Error(CodeUnknownProvider)
	}
	r.providers[name] = p
	r.order = append(r.order, name)
	return nil
}

// Names lists the registered providers in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Default reports the configured default provider name.
func (r *Registry) Default() string { return r.opts.Default }

// Resolve returns the provider for a name. "off" and unknown names are errors:
// the caller must not fall back to anything weaker on its own.
func (r *Registry) Resolve(name string) (Provider, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = r.opts.Default
	}
	if name == "" || name == ProviderOff {
		return nil, Error(CodeDisabled)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	if !ok {
		return nil, Error(CodeUnknownProvider)
	}
	return p, nil
}

// Info returns one catalogue row, probing if the cache is cold.
func (r *Registry) Info(ctx context.Context, name string) (ProviderInfo, error) {
	p, err := r.Resolve(name)
	if err != nil {
		return ProviderInfo{}, err
	}
	return r.info(ctx, p), nil
}

// Catalog returns every registered provider, ordered for display: the default
// first, then by isolation layer, then by name.
func (r *Registry) Catalog(ctx context.Context) []ProviderInfo {
	r.mu.RLock()
	providers := make([]Provider, 0, len(r.order))
	for _, name := range r.order {
		providers = append(providers, r.providers[name])
	}
	r.mu.RUnlock()

	out := make([]ProviderInfo, 0, len(providers))
	for _, p := range providers {
		out = append(out, r.info(ctx, p))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Available != out[j].Available {
			return out[i].Available
		}
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		li, lj := layerRank(out[i].Layer), layerRank(out[j].Layer)
		if li != lj {
			return li < lj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ProbeAll refreshes every entry, which the console's refresh button uses.
func (r *Registry) ProbeAll(ctx context.Context) []ProviderInfo {
	r.probeMu.Lock()
	r.probeCache = map[string]probeEntry{}
	r.probeMu.Unlock()
	return r.Catalog(ctx)
}

// RequireAvailable resolves a provider and refuses one that cannot run here.
// Registration validation and lease acquisition both go through it, so a
// resource can never be saved pointing at a backend this host does not have.
func (r *Registry) RequireAvailable(ctx context.Context, name string) (Provider, ProviderInfo, error) {
	p, err := r.Resolve(name)
	if err != nil {
		return nil, ProviderInfo{}, err
	}
	info := r.info(ctx, p)
	if !info.Available {
		return nil, info, Error(CodeUnsupported)
	}
	return p, info, nil
}

func (r *Registry) info(ctx context.Context, p Provider) ProviderInfo {
	name := p.Name()
	r.probeMu.Lock()
	if entry, ok := r.probeCache[name]; ok && r.opts.Clock().Sub(entry.at) < r.opts.ProbeTTL {
		r.probeMu.Unlock()
		return entry.info
	}
	r.probeMu.Unlock()

	desc, derr := p.Describe(ctx)
	if derr != nil {
		desc = Descriptor{Name: name, Layer: LayerNone, Isolation: IsolationNone, Source: SourceBuiltin}
	}
	desc.Name = name
	desc.Default = name == r.opts.Default

	probeCtx, cancel := context.WithTimeout(ctx, r.opts.ProbeTimeout)
	defer cancel()
	avail, aerr := p.Probe(probeCtx)
	if aerr != nil {
		avail = Availability{Available: false, Reason: aerr.Error()}
	}
	avail.CheckedAt = r.opts.Clock()
	info := ProviderInfo{Descriptor: desc, Availability: avail}

	r.probeMu.Lock()
	r.probeCache[name] = probeEntry{info: info, at: avail.CheckedAt}
	r.probeMu.Unlock()
	return info
}

// layerRank orders the catalogue the way docs/sandbox-landscape.md lists it.
func layerRank(l Layer) int {
	for i, v := range Layers {
		if v == l {
			return i
		}
	}
	return len(Layers)
}
