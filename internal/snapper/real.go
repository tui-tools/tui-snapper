package snapper

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tui-tools/tui-kit/runner"
)

// Real drives snapper on the host. It satisfies Backend.
//
// Two binaries are involved and each gets its own runner. snapper does the
// work; systemctl is asked, read-only and unprivileged, whether the timeline
// and cleanup timers are running. Only snapper escalates.
//
// Unlike systemctl, snapper's reads are privileged: listing a config's
// snapshots opens the subvolume, which an ordinary user is not allowed to do.
// So the snapper runner keeps the kit's default of escalating reads too, and
// a machine where that fails says why rather than showing an empty list.
type Real struct {
	snapper   *runner.Runner
	systemctl *runner.Runner
	// systemctlErr holds why the timer states cannot be read, so the screen
	// can say so instead of showing blank rows.
	systemctlErr error
	// bootConfig is the limine configuration the boot entries are read
	// from. It is a field so a test can point it at a fixture.
	bootConfig string
	// globals are the snapper-wide flags every invocation carries. They are
	// part of the argv the dialog previews, so what runs is still exactly
	// what was shown.
	globals []string
}

// readTimeout bounds a read. Listing snapshots is fast; a diff of a large
// file on a cold cache is not.
const readTimeout = 30 * time.Second

// actionTimeout bounds a mutation. Deleting a snapshot on a busy filesystem,
// and `cleanup` deleting several, can take a while.
const actionTimeout = 5 * time.Minute

// unprivileged is the address-of-false the runner options need.
var unprivileged = false

// DefaultBootConfig is where limine keeps its configuration on an
// Omarchy-style layout.
const DefaultBootConfig = "/boot/limine.conf"

// Available reports whether snapper is installed on this host.
func Available() bool {
	return runner.Available("snapper", "/usr/bin/snapper", "/sbin/snapper")
}

// New builds the real backend. sudoPrefix comes from the configuration
// ("sudo -n"); pass nil to run snapper directly. Only snapper is required: a
// machine without systemd still gets every snapshot view, and the timers
// screen says what is missing.
func New(sudoPrefix []string) (*Real, error) {
	sn, err := runner.New(runner.Options{
		Bin:         "snapper",
		SearchPaths: []string{"/usr/bin/snapper", "/sbin/snapper", "/usr/sbin/snapper"},
		SudoPrefix:  sudoPrefix,
		Timeout:     actionTimeout,
		InstallHint: "install the snapper package; use --demo to explore the UI",
	})
	if err != nil {
		return nil, err
	}

	r := &Real{snapper: sn, bootConfig: DefaultBootConfig}
	r.systemctl, r.systemctlErr = runner.New(runner.Options{
		Bin:             "systemctl",
		SearchPaths:     []string{"/usr/bin/systemctl", "/bin/systemctl"},
		Timeout:         readTimeout,
		PrivilegedReads: &unprivileged,
	})
	return r, nil
}

// Name identifies the backend.
func (r *Real) Name() string { return "snapper" }

// Describe is the one-line summary shown in the header.
func (r *Real) Describe() string { return r.snapper.Describe() }

// Preview renders the exact command line Run will execute.
func (r *Real) Preview(cmd runner.Command) string { return r.snapper.Preview(cmd) }

// Run executes a previewed command.
func (r *Real) Run(ctx context.Context, cmd runner.Command) (string, error) {
	return r.snapper.Run(ctx, cmd)
}

// Build turns an action into a previewable command, carrying the same global
// flags the reads use.
func (r *Real) Build(spec ActionSpec, req Request) (runner.Command, error) {
	req.Globals = r.globals
	return BuildCommand(spec, req)
}

// SetNoDBus makes every snapper invocation carry --no-dbus, which is what a
// machine with no running snapperd needs: a container, a chroot, or a rescue
// shell. The flag is part of the previewed argv, so the dialog still shows
// exactly what will run.
func (r *Real) SetNoDBus(enabled bool) {
	r.globals = nil
	if enabled {
		r.globals = []string{"--no-dbus"}
	}
}

// Configs lists the snapper configurations.
func (r *Real) Configs(ctx context.Context) ([]Config, error) {
	out, err := r.read(ctx, ListConfigsArgs(r.globals)...)
	if err != nil {
		return nil, err
	}
	return ParseConfigs(out)
}

// Snapshots lists one config's snapshots, newest first.
func (r *Real) Snapshots(ctx context.Context, config string) ([]Snapshot, error) {
	if config == "" {
		return nil, fmt.Errorf("no config selected")
	}
	out, err := r.read(ctx, ListArgs(r.globals, config)...)
	if err != nil {
		return nil, err
	}
	return ParseSnapshots(out)
}

// Status lists what changed between two snapshots.
func (r *Real) Status(ctx context.Context, config string, from, to int) ([]Change, error) {
	if config == "" {
		return nil, fmt.Errorf("no config selected")
	}
	if from == to {
		return nil, fmt.Errorf("a comparison needs two different snapshots")
	}
	out, err := r.read(ctx, StatusArgs(r.globals, config, from, to)...)
	if err != nil {
		return nil, err
	}
	return ParseStatus(out), nil
}

// Diff returns one path's unified diff between two snapshots.
func (r *Real) Diff(ctx context.Context, config string, from, to int, path string) (string, error) {
	if config == "" {
		return "", fmt.Errorf("no config selected")
	}
	if path == "" {
		return "", fmt.Errorf("no file selected")
	}
	return r.read(ctx, DiffArgs(r.globals, config, from, to, path)...)
}

// Timers reports the state of snapper's systemd timers.
func (r *Real) Timers(ctx context.Context) []TimerState {
	states := make([]TimerState, 0, len(TimerUnits))
	for _, unit := range TimerUnits {
		if r.systemctlErr != nil {
			states = append(states, TimerState{
				Unit: unit,
				Err:  "systemctl is not available: " + r.systemctlErr.Error(),
			})
			continue
		}
		out, err := r.systemctl.Read(ctx, "systemctl", "show", unit,
			"--property=ActiveState", "--property=UnitFileState", "--no-pager")
		if err != nil {
			states = append(states, TimerState{Unit: unit, Err: err.Error()})
			continue
		}
		states = append(states, ParseTimerState(unit, out))
	}
	return states
}

// Platform reports how this machine rolls back.
//
// The question is not "which distribution is this": it is which of two real
// mechanisms is in front of the user. snapper's own rollback only exists for
// the root filesystem and only in a build carrying the rollback flag; the
// Omarchy and limine-snapper-sync layouts instead put every snapshot in the
// boot menu, and booting one is the rollback. Both are detected from evidence
// on the machine, and Reason says which evidence was used.
func (r *Real) Platform(ctx context.Context, config Config) Platform {
	p := Platform{BootConfig: r.bootConfig}
	if out, err := r.read(ctx, WithGlobals(r.globals, "--version")...); err == nil {
		p.SnapperFlags = versionFlags(out)
	}

	// A boot menu that already lists the snapshots is the mechanism the user
	// will actually use, so it is checked first.
	if entries, err := ReadBootEntries(r.bootConfig); err == nil && len(entries) > 0 {
		p.Kind = RollbackBootMenu
		p.Entries = entries
		p.Reason = fmt.Sprintf(
			"%s lists %d snapshot entries, so this machine rolls back from the boot menu.",
			r.bootConfig, len(entries))
		return p
	}

	switch {
	case config.Subvolume != "/":
		p.Kind = RollbackUnsupported
		p.Reason = fmt.Sprintf(
			"snapper rollback only applies to the root filesystem, and this config protects %s.",
			config.Subvolume)
	case !p.HasRollbackFlag():
		p.Kind = RollbackUnsupported
		p.Reason = "this snapper build does not carry the rollback flag, so `snapper rollback` is not available."
	default:
		p.Kind = RollbackSnapper
		p.Reason = "this config is the root filesystem and snapper was built with rollback support."
	}
	if _, err := os.Stat(r.bootConfig); err == nil && p.Kind != RollbackBootMenu {
		p.Reason += " " + r.bootConfig +
			" exists but lists no snapshot entries."
	}
	return p
}

// read runs a read-only snapper invocation and turns the two failures worth
// explaining into advice rather than a raw error.
func (r *Real) read(ctx context.Context, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	out, err := r.snapper.Read(ctx, argv...)
	if err != nil {
		return out, r.explain(err)
	}
	return out, nil
}

// explain rewrites the two errors a user hits first into something they can
// act on. snapper's reads open the subvolume directly, so an unprivileged
// read fails with an errno rather than with a permission message anyone
// recognises.
func (r *Real) explain(err error) error {
	text := err.Error()
	switch {
	case strings.Contains(text, "Operation not permitted"),
		strings.Contains(text, "Permission denied"):
		return fmt.Errorf(
			"%w — snapper reads the subvolume directly, so this needs root: "+
				"run tui-snapper with sudo, or set sudo = \"sudo -n\" in the config", err)
	case strings.Contains(text, "not found"):
		// "Config 'x' not found" is snapper's own wording and is already
		// clear; it is passed through untouched.
		return err
	default:
		return err
	}
}

// SetBootConfig points the boot-entry reader at a different limine
// configuration, which is what the `boot-config` setting is for.
func (r *Real) SetBootConfig(path string) {
	if path != "" {
		r.bootConfig = path
	}
}
