package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-snapper/internal/snapper"
)

// checkTimeout bounds the read. Listing a config's snapshots opens the
// subvolume and a machine whose snapperd is wedged must not hang a
// non-interactive check forever.
const checkTimeout = 60 * time.Second

// checkReport is what --check prints: the model the backend parsed, plus the
// counts and the platform verdict a test can assert on without walking the
// whole structure.
//
// It is a report of the read path only. --check never builds and never runs a
// mutation, so it is safe to run anywhere — including against a machine whose
// snapshots someone cares about.
type checkReport struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Backend string `json:"backend"`
	// Describe is the backend's own one-line summary, which is where the
	// escalation prefix in use becomes visible.
	Describe string `json:"describe"`
	// Config is the configuration the snapshots below were read from, and
	// Configs is how many the machine has.
	Config     string           `json:"config"`
	Subvolume  string           `json:"subvolume"`
	Configs    int              `json:"configs"`
	ConfigList []snapper.Config `json:"config_list"`
	// Snapshots is the row count including snapper's number 0, the live
	// subvolume; Real excludes it, and is the figure that matches what a
	// person counts in `snapper list`.
	Snapshots int `json:"snapshots"`
	Real      int `json:"real_snapshots"`
	// Settings is what `snapper get-config` reports for the retention keys
	// this tool can write, and SettingsErr why it could not be read. Only
	// those keys: a config also carries the users and groups allowed to use
	// it, and a --check output gets pasted into public issues.
	Settings    map[string]string `json:"settings"`
	SettingsErr string            `json:"settings_error,omitempty"`
	// Rollback is how this machine rolls back, which is the one thing that
	// genuinely differs between an openSUSE-style layout and an Omarchy one.
	Rollback   string `json:"rollback"`
	Reason     string `json:"rollback_reason"`
	BootConfig string `json:"boot_config"`
	// BootEntries is the snapshot subtree of the boot menu, read-only. On a
	// limine layout an empty list here with snapshots above it means the
	// parser failed, not that the machine has nothing to boot.
	BootEntries []snapper.BootEntry `json:"boot_entries"`
	// SnapperFlags are the feature flags `snapper --version` reported.
	SnapperFlags []string `json:"snapper_flags"`
	// Compat is what the backend version probe found. It is reported rather
	// than asserted: an untested version is a fact about the machine, not a
	// failure of the read path. It is also where the smoke test reads the
	// version it records as compatibility evidence.
	Compat compat.Result `json:"compat"`
	// Timers is the read-only state of the timeline and cleanup units.
	Timers []snapper.TimerState `json:"timers"`
	// Model is the parsed snapshot list in full.
	Model []snapper.Snapshot `json:"model"`
}

// runCheck exercises the backend's real read path and prints the parsed model
// as JSON. wanted is the config to read; empty picks the first one the
// machine reports, which is what the UI opens on.
//
// It returns an error when the backend cannot be read, which main turns into
// a non-zero exit — so a caller can treat the exit code alone as the verdict.
func runCheck(backend snapper.Backend, wanted string,
	backendCompat compat.Result, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	configs, err := backend.Configs(ctx)
	if err != nil {
		return fmt.Errorf("%s backend read failed: %w", backend.Name(), err)
	}
	if len(configs) == 0 {
		return fmt.Errorf("%s reported no configurations", backend.Name())
	}

	// The same precedence the UI uses: the named config when it exists, and
	// otherwise the first one, so --check and the TUI never disagree about
	// which config is being shown.
	config := configs[0]
	if wanted != "" {
		found := false
		for _, candidate := range configs {
			if candidate.Name == wanted {
				config, found = candidate, true
				break
			}
		}
		if !found {
			return fmt.Errorf("config %q not found on this machine", wanted)
		}
	}

	snapshots, err := backend.Snapshots(ctx, config.Name)
	if err != nil {
		return fmt.Errorf("reading config %q failed: %w", config.Name, err)
	}
	platform := backend.Platform(ctx, config)

	// The settings read is reported rather than fatal: a machine whose
	// get-config this build cannot read still has a working snapshot view,
	// and the reason belongs in the report instead of in an exit code.
	settings, settingsErr := readEditableSettings(ctx, backend, config.Name)

	report := checkReport{
		Tool:         toolName,
		Version:      version,
		Backend:      backend.Name(),
		Describe:     backend.Describe(),
		Config:       config.Name,
		Subvolume:    config.Subvolume,
		Configs:      len(configs),
		ConfigList:   configs,
		Snapshots:    len(snapshots),
		Settings:     settings,
		SettingsErr:  settingsErr,
		Rollback:     string(platform.Kind),
		Reason:       platform.Reason,
		BootConfig:   platform.BootConfig,
		BootEntries:  platform.Entries,
		SnapperFlags: platform.SnapperFlags,
		Compat:       backendCompat,
		Timers:       backend.Timers(ctx),
		Model:        snapshots,
	}
	for _, snapshot := range snapshots {
		if !snapshot.Current() {
			report.Real++
		}
	}

	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// readEditableSettings reads a config's settings and keeps only the keys the
// tool can write, so the report says what the retention form would open on.
func readEditableSettings(ctx context.Context, backend snapper.Backend,
	config string) (map[string]string, string) {
	settings, err := backend.Settings(ctx, config)
	if err != nil {
		return nil, err.Error()
	}
	kept := map[string]string{}
	for _, setting := range snapper.EditableSettings {
		if value, ok := settings[setting.Key]; ok {
			kept[setting.Key] = value
		}
	}
	return kept, ""
}
