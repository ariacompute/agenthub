package adapter

import (
	"context"
	"encoding/hex"
	"hash/fnv"
	"strings"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/registry"
)

// Every mock connector is deterministic: the same request always produces the
// same bytes, which keeps the whole test suite assertion-friendly and stable
// under -race.

func digest(s string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

func pick(s string, n int) int {
	if n <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32() % uint32(n))
}

func tokenCount(prompt string) int {
	return len(strings.Fields(prompt)) + pick(prompt, 8)
}

// truncate caps a payload, returning the (possibly shortened) value and whether
// truncation happened. Adapters never silently drop data: truncation is
// reported through the artifact metadata.
func truncate(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	return s[:max], true
}

func meta(pairs ...string) map[string]any {
	if len(pairs) == 0 {
		return nil
	}
	out := map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i]] = pairs[i+1]
	}
	return out
}

func artifact(name string, index int, parts []a2a.Part, kv ...string) a2a.Artifact {
	last := true
	return a2a.Artifact{
		ArtifactID: name + "-" + itoa(index),
		Name:       name,
		Parts:      parts,
		Index:      index,
		LastChunk:  &last,
		Metadata:   meta(kv...),
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func metaString(m map[string]string, key string) string {
	if m == nil {
		return ""
	}
	return m[key]
}

// mockHealth derives a stable health for an entry. Real connectors replace this
// with a probe; the mock keeps the same signature so the hub never branches.
func mockHealth(e *registry.Entry) registry.Health {
	if e == nil {
		return registry.HealthUnknown
	}
	if v, err := registry.ParseHealth(metaString(e.Metadata, "mock_health")); err == nil && v != "" {
		return v
	}
	if strings.TrimSpace(e.Endpoint) == "" {
		return registry.HealthDegraded
	}
	return registry.HealthHealthy
}

// boundedContext applies the adapter timeout shared by mock and real connectors.
func boundedContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}
