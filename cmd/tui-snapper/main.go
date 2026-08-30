// Command tui-snapper is a terminal UI for btrfs snapshots managed by
// snapper. It lists the configs and their snapshots, compares any two of
// them, and creates, deletes, relabels and cleans them up — with every
// mutation shown as an exact snapper command line and confirmed first.
//
// It is deliberately generic: any distribution with snapper works, and the
// one thing that really differs between an openSUSE-style layout and an
// Omarchy one — how a rollback is performed — is detected and explained
// rather than assumed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-snapper/internal/snapper"
)

// toolName is the binary name, which is also the configuration directory:
// /etc/tui-snapper/config.toml and ~/.config/tui-snapper/config.toml.
const toolName = "tui-snapper"

// This tool's own configuration keys.
const (
	// keyConfig is the snapper config to open on; empty picks the first one.
	keyConfig = "config"
	// keyBootConfig is the limine configuration the boot menu entries are
	// read from, on a layout that has one.
	keyBootConfig = "boot-config"
	// keyNoDBus makes every snapper call carry --no-dbus, for a machine
	// with no running snapperd.
	keyNoDBus = "no-dbus"
)

// version is stamped by the release build (-ldflags "-X main.version=…").
var version = "dev"

// defaults declares the configuration keys the tool understands. Only these
// are read from the environment (TUI_SNAPPER_CONFIG, …), so an unrelated
// variable can never leak into the configuration.
func defaults() map[string]string {
	return map[string]string{
		keyConfig:       "",
		keyBootConfig:   snapper.DefaultBootConfig,
		keyNoDBus:       "false",
		config.KeySudo:  "sudo -n",
		config.KeyTheme: "",
	}
}

// options holds the parsed command line.
type options struct {
	demo        bool
	check       bool
	report      bool
	config      string
	bootConfig  string
	noDBus      bool
	themePath   string
	sudo        string
	showVersion bool
	// sudoSet records whether -sudo was passed, so `--sudo ""` can disable
	// escalation instead of reading as "not given".
	sudoSet bool
}

// parseFlags defines and reads the command line.
func parseFlags(args []string, out *os.File) (options, error) {
	var opts options
	fs := flag.NewFlagSet(toolName, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.BoolVar(&opts.demo, "demo", false,
		"run against sample data, without touching anything")
	fs.BoolVar(&opts.check, "check", false,
		"read the backend, print the parsed model as JSON and exit")
	fs.BoolVar(&opts.report, "report", false, reportUsage)
	fs.StringVar(&opts.config, "config", "",
		"snapper config to open on (overrides the config file)")
	fs.StringVar(&opts.bootConfig, "boot-config", "",
		"limine configuration to read the boot menu entries from")
	fs.BoolVar(&opts.noDBus, "no-dbus", false,
		"pass --no-dbus to snapper, for a machine with no running snapperd")
	fs.StringVar(&opts.themePath, "theme", "",
		"path to an Omarchy-style colors.toml (overrides the config file)")
	fs.StringVar(&opts.sudo, "sudo", "",
		"privilege escalation prefix, e.g. \"sudo -n\" or \"\" to disable")
	fs.BoolVar(&opts.showVersion, "version", false, "print the version and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(out, "tui-snapper — btrfs snapshots, managed by snapper\n\n"+
			"Usage:\n  tui-snapper [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(out, "\nsnapper reads its subvolumes directly, so this tool "+
			"needs root: run it with sudo,\nor leave sudo = \"sudo -n\" configured. "+
			"--demo needs neither.\n")
		_, _ = fmt.Fprintf(out, "\nConfiguration is read from %s, then %s, "+
			"then TUI_SNAPPER_* in the environment.\n",
			config.SystemPathFor(toolName), config.UserPathFor(toolName))
	}
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "sudo" {
			opts.sudoSet = true
		}
	})
	return opts, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, toolName+":", err)
		os.Exit(1)
	}
}

// run wires the configuration, the backend and the Bubble Tea program.
func run(args []string) error {
	opts, err := parseFlags(args, os.Stdout)
	if err != nil {
		// flag already printed the reason and the usage.
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if opts.showVersion {
		fmt.Println(toolName, version)
		return nil
	}

	cfg, err := config.Load(config.Options{Tool: toolName, Defaults: defaults()})
	if err != nil {
		return err
	}
	applyOverrides(&cfg, opts)

	// The configured theme is handed to the kit through the same variable the
	// user could set by hand, so precedence stays in one place. It is set
	// before the backend is built so --report can name the theme the UI would
	// have used even on a machine where no backend can be.
	if path := cfg.Theme(); path != "" {
		if err := os.Setenv("TUI_THEME", path); err != nil {
			return err
		}
	}

	// --report is the non-interactive path that must work everywhere. It reads
	// nothing privileged and it survives a machine with no snapper at all,
	// because "there is nothing here to drive" is one of the things a bug
	// report has to be able to say. So it comes before the backend is
	// required.
	if opts.report {
		return runReport(cfg, opts, os.Stdout)
	}

	backend, err := pickBackend(cfg, opts)
	if err != nil {
		return err
	}

	wanted := cfg.String(keyConfig, "")

	// The backend version is probed once, here, and used by both paths: the
	// header shows it, --check reports it, and the smoke test records it as
	// compatibility evidence.
	backendCompat := probeCompat(context.Background(), backend.Name(), opts.demo)

	// --check is the non-interactive path: it runs the backend's real read
	// path and prints, and never starts a terminal program. It is checked
	// after the backend is built so that "snapper is not installed" is still
	// reported as the failure it is.
	if opts.check {
		return runCheck(backend, wanted, backendCompat, os.Stdout)
	}

	program := tea.NewProgram(
		newApp(backend, theme.New(), wanted, backendCompat), tea.WithAltScreen())
	_, err = program.Run()
	return err
}

// applyOverrides folds the command line into the configuration, which is the
// last and highest-precedence layer.
func applyOverrides(cfg *config.Config, opts options) {
	if opts.config != "" {
		cfg.Set(keyConfig, opts.config)
	}
	if opts.bootConfig != "" {
		cfg.Set(keyBootConfig, opts.bootConfig)
	}
	if opts.noDBus {
		cfg.Set(keyNoDBus, "true")
	}
	if opts.themePath != "" {
		cfg.Set(config.KeyTheme, opts.themePath)
	}
	// An explicitly empty -sudo disables escalation, so the flag is applied
	// whenever it was passed, empty value included.
	if opts.sudoSet {
		cfg.Set(config.KeySudo, opts.sudo)
	}
}

// pickBackend returns the demo backend or the real one.
func pickBackend(cfg config.Config, opts options) (snapper.Backend, error) {
	if opts.demo {
		return snapper.NewFake(), nil
	}
	real, err := snapper.New(cfg.SudoPrefix())
	if err != nil {
		return nil, err
	}
	real.SetBootConfig(cfg.String(keyBootConfig, snapper.DefaultBootConfig))
	real.SetNoDBus(cfg.Bool(keyNoDBus, false))
	return real, nil
}
