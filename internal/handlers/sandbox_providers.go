package handlers

import (
	"net/http"

	"agenthub/internal/httputil"
	"agenthub/internal/sandbox"
)

// sandboxLayerGroup is one row of the console's catalogue: a layer and the
// providers that claim it. Grouping happens here rather than in the browser so
// the presentation order (container first, "none" last) is decided once, next to
// the enum that defines it.
type sandboxLayerGroup struct {
	Layer     sandbox.Layer          `json:"layer"`
	Providers []sandbox.ProviderInfo `json:"providers"`
}

// handleListSandboxProviders returns the catalogue grouped by isolation layer,
// along with the instance default. An entry that cannot run here is present and
// unavailable with a reason ("bwrap is not installed"): the console must be able
// to show what could be enabled, not only what already works.
func (d Deps) handleListSandboxProviders(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	catalog := d.Sandboxes.Catalog(r.Context())
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"providers": catalog,
		"groups":    groupByLayer(catalog),
		"layers":    sandbox.Layers,
		"default":   d.Sandboxes.Registry().Default(),
	})
}

// handleRefreshSandboxProviders clears the probe cache and reports the fresh
// result. It is a POST because it spawns the runtime binaries: the console's
// refresh button should be the only thing that pays for it, and a cached GET must
// stay side-effect free.
func (d Deps) handleRefreshSandboxProviders(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	catalog := d.Sandboxes.RefreshCatalog(r.Context())
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"providers": catalog,
		"groups":    groupByLayer(catalog),
		"layers":    sandbox.Layers,
		"default":   d.Sandboxes.Registry().Default(),
	})
}

// groupByLayer buckets the catalogue in the declared layer order, skipping empty
// layers so the console never renders a heading with nothing under it.
func groupByLayer(catalog []sandbox.ProviderInfo) []sandboxLayerGroup {
	buckets := make(map[sandbox.Layer][]sandbox.ProviderInfo, len(sandbox.Layers))
	for _, info := range catalog {
		buckets[info.Layer] = append(buckets[info.Layer], info)
	}
	groups := make([]sandboxLayerGroup, 0, len(buckets))
	for _, layer := range sandbox.Layers {
		if len(buckets[layer]) == 0 {
			continue
		}
		groups = append(groups, sandboxLayerGroup{Layer: layer, Providers: buckets[layer]})
	}
	return groups
}
