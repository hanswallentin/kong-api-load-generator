package kong

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kong/kong-loadgen/src/config"
)

// appliedFile records the fingerprint of the configuration last pushed to
// Konnect. It lives in generated/, which is git-ignored.
const appliedFile = "applied.json"

type applied struct {
	AppliedAt time.Time         `json:"applied_at"`
	Parts     map[string]string `json:"parts"`
}

// Drift describes how the configuration differs from what configure last pushed.
type Drift struct {
	NeverConfigured bool     // configure has not pushed anything yet
	Changed         []string // parts that differ, in Parts order
}

// Clean reports whether the configuration matches what was pushed.
func (d Drift) Clean() bool {
	return !d.NeverConfigured && len(d.Changed) == 0
}

// CheckDrift compares the configuration with the record left by configure.
func CheckDrift(cfg config.Config, configPath string) (Drift, error) {
	current, err := Fingerprint(cfg)
	if err != nil {
		return Drift{}, err
	}
	path := filepath.Join(generatedDirectory(configPath), appliedFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Drift{NeverConfigured: true}, nil
	}
	if err != nil {
		return Drift{}, fmt.Errorf("read %s: %w", path, err)
	}
	var record applied
	if err := json.Unmarshal(data, &record); err != nil {
		return Drift{}, fmt.Errorf("parse %s: %w", path, err)
	}
	var changed []string
	for _, part := range Parts {
		if record.Parts[part] != current[part] {
			changed = append(changed, part)
		}
	}
	sort.Strings(changed)
	return Drift{Changed: changed}, nil
}

func recordApplied(directory string, parts map[string]string) error {
	data, err := json.MarshalIndent(applied{AppliedAt: time.Now().UTC(), Parts: parts}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode applied record: %w", err)
	}
	if err := os.WriteFile(filepath.Join(directory, appliedFile), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write applied record: %w", err)
	}
	return nil
}

func clearApplied(directory string) error {
	err := os.Remove(filepath.Join(directory, appliedFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove applied record: %w", err)
	}
	return nil
}

func generatedDirectory(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "generated")
}
