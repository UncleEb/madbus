// Package state persists the one piece of durable runtime state Madbus keeps:
// each device's last-online timestamp (the time of its most recent successful
// poll). Everything else — current readings — lives in memory and is rebuilt by
// polling. There is deliberately no database; this is a single small JSON file.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// fileFormat is the on-disk shape: device id -> RFC3339 last-online timestamp.
type fileFormat map[string]string

// Load reads last-online timestamps from path. A missing file is not an error
// (first run). Individual unparseable entries are skipped rather than failing
// startup.
func Load(path string) (map[string]time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]time.Time{}, nil
		}
		return nil, err
	}
	var raw fileFormat
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make(map[string]time.Time, len(raw))
	for id, ts := range raw {
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			continue
		}
		out[id] = t
	}
	return out, nil
}

// Save atomically writes last-online timestamps to path (temp file + rename, so
// a crash mid-write can't corrupt the file). Zero timestamps are omitted.
func Save(path string, lastOnline map[string]time.Time) error {
	raw := make(fileFormat, len(lastOnline))
	for id, t := range lastOnline {
		if t.IsZero() {
			continue
		}
		raw[id] = t.UTC().Format(time.RFC3339)
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".last_seen-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed away

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
