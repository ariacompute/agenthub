package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// SnapshotVersion is the on-disk format revision.
const SnapshotVersion = 1

// Snapshot is the persisted catalogue.
type Snapshot struct {
	Version int      `json:"version"`
	Entries []*Entry `json:"entries"`
}

// LoadSnapshot reads the catalogue from disk. A missing file is not an error:
// agenthub starts with an empty registry and fills it as resources register.
func LoadSnapshot(path string) (*Snapshot, error) {
	if path == "" {
		return &Snapshot{Version: SnapshotVersion}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Snapshot{Version: SnapshotVersion}, nil
		}
		return nil, fmt.Errorf("registry: read snapshot: %w", err)
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("registry: decode snapshot: %w", err)
	}
	if s.Entries == nil {
		s.Entries = []*Entry{}
	}
	return &s, nil
}

// SaveSnapshot writes the catalogue atomically (temp file + rename).
func SaveSnapshot(path string, r *Registry) error {
	if path == "" || r == nil {
		return nil
	}
	s := Snapshot{Version: SnapshotVersion, Entries: r.Snapshot()}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("registry: encode snapshot: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("registry: create snapshot dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("registry: write snapshot: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("registry: rename snapshot: %w", err)
	}
	return nil
}

// LoadInto replaces the registry contents from the snapshot on disk.
func LoadInto(path string, r *Registry) error {
	s, err := LoadSnapshot(path)
	if err != nil {
		return err
	}
	r.ReplaceAll(s.Entries)
	if len(s.Entries) > 0 {
		slog.Info("registry: snapshot loaded", "path", path, "entries", len(s.Entries))
	}
	return nil
}
