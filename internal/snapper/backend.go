package snapper

import (
	"context"

	"github.com/tui-tools/tui-kit/runner"
)

// Backend is the boundary between the UI and the machine. The read methods
// return the model; Build turns an intent into a previewable Command; Run
// executes a Command the user confirmed. Nothing else may mutate anything.
type Backend interface {
	// Name identifies the backend ("snapper", "demo").
	Name() string
	// Describe is the one-line summary shown in the header.
	Describe() string

	// Configs lists the snapper configurations on this machine.
	Configs(ctx context.Context) ([]Config, error)
	// Settings reads one config's settings, keyed by snapper's own key names.
	Settings(ctx context.Context, config string) (map[string]string, error)
	// CheckSubvolume reports whether a path can hold a new snapper config.
	// The error explains what is wrong with it, for the dialog.
	CheckSubvolume(ctx context.Context, path string) error
	// Snapshots lists one config's snapshots, newest first.
	Snapshots(ctx context.Context, config string) ([]Snapshot, error)
	// Status lists what changed between two snapshots.
	Status(ctx context.Context, config string, from, to int) ([]Change, error)
	// Diff returns one path's unified diff between two snapshots, as text.
	Diff(ctx context.Context, config string, from, to int, path string) (string, error)
	// Timers reports the state of snapper's systemd timers, read-only.
	Timers(ctx context.Context) []TimerState
	// Platform reports how this machine rolls back, for the given config.
	Platform(ctx context.Context, config Config) Platform

	// Build turns an action and its collected values into a previewable
	// command.
	Build(spec ActionSpec, req Request) (runner.Command, error)
	// Preview renders the exact command line Run will execute.
	Preview(cmd runner.Command) string
	// Run executes a previously previewed command.
	Run(ctx context.Context, cmd runner.Command) (string, error)
}
