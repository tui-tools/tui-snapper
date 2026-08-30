package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/report"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-snapper/internal/snapper"
)

// runReport prints the block a bug report needs and exits. Everything generic
// — the kit version, the distribution, the kernel, the terminal, where the
// binary came from — is collected by the kit, so the whole family answers
// --report in the same shape. What this function adds is the part only
// tui-snapper knows: the backend it selected, the version the same probe
// --check uses read off it, and the three settings that decide what every
// snapper command line looks like.
//
// It reads no snapshot. --check is the flag that does that, and on this tool
// it needs root for even a listing; a report has to work for a user who cannot
// get it, because the missing privilege may well be the bug. For the same
// reason a machine with no snapper at all still gets a report, with the
// selection error as one of its lines: "there is nothing here to drive" is a
// bug report, not a refusal.
func runReport(cfg config.Config, opts options, out io.Writer) error {
	palette, _ := theme.ResolvePalette()

	var backendName, selectError string
	if backend, err := pickBackend(cfg, opts); err != nil {
		selectError = err.Error()
	} else {
		backendName = backend.Name()
	}

	// The same probe --check and the header use. There is one version probe in
	// this tool and this is it.
	backendCompat := probeCompat(context.Background(), backendName, opts.demo)

	info := report.Info{
		Tool:           toolName,
		Version:        version,
		Backend:        backendName,
		BackendVersion: backendCompat.Version,
		BackendDetail:  backendCompat.Detail,
		Demo:           opts.demo,
		Sudo:           cfg.String(config.KeySudo, ""),
		Theme:          palette.Name,
	}
	if opts.demo {
		// The fake answers to "demo", and the block says so on its own line;
		// what it imitates is snapper itself, which is the parser and the
		// command builders the session exercised.
		info.Backend = "demo"
		info.Extra = append(info.Extra, report.Field{
			Key: "demo backend", Value: realBackendName,
		})
	}

	// The settings that change every command line this tool builds, and so
	// change what a reproduction has to look like: which config is opened,
	// whether snapper is called with --no-dbus, and where the boot menu is
	// read from on a limine layout.
	info.Extra = append(info.Extra,
		report.Field{Key: "snapper config", Value: configLine(cfg)},
		report.Field{Key: "no-dbus", Value: yesNo(cfg.Bool(keyNoDBus, false))},
		report.Field{Key: "boot config",
			Value: scrubPath(cfg.String(keyBootConfig, snapper.DefaultBootConfig))},
	)
	if selectError != "" {
		info.Extra = append(info.Extra, report.Field{
			Key: "backend error", Value: selectError,
		})
	}

	_, err := io.WriteString(out, report.Render(info))
	return err
}

// realBackendName is what the real backend calls itself, and so what the fake
// imitates. It is the same string snapper.Real.Name() returns.
const realBackendName = "snapper"

// configLine names the snapper config the tool would open on. An unset config
// is not an omission: it means the UI takes whichever one the machine reports
// first, and a bug about the wrong config being shown starts there.
func configLine(cfg config.Config) string {
	if name := strings.TrimSpace(cfg.String(keyConfig, "")); name != "" {
		return name
	}
	return "(first on the machine)"
}

// scrubPath keeps the block publishable. The boot configuration is a path the
// user can point anywhere, so one that would name them — anything under /home
// or /root — is replaced rather than printed. Everywhere else the path is the
// fact: `/boot/limine.conf` and a custom location are different reports.
func scrubPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	for _, dir := range []string{"/home", "/root"} {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return "(elsewhere)"
		}
	}
	return path
}

// yesNo renders a setting the way the block reads it.
func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// reportUsage is the flag's one-line help, kept here next to what it prints.
var reportUsage = fmt.Sprintf(
	"print the versions and machine facts a bug report needs, then exit "+
		"(no UI, no privileges, nothing about you: paste it into a %s issue)",
	toolName)
