package main

import (
	"context"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
	"github.com/tui-tools/tui-kit/theme"
	tuisnapper "github.com/tui-tools/tui-snapper"
	"github.com/tui-tools/tui-snapper/internal/snapper"
)

// testCompat probes the manifest's real snapper backend against a canned
// `snapper --version` output, so the header is exercised with the same block
// the binary ships rather than with a hand-written fixture.
func testCompat(t *testing.T, versionOutput string) compat.Result {
	t.Helper()
	m, err := manifest.Load(tuisnapper.ManifestJSON)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	backend, ok := m.Backend("snapper")
	if !ok {
		t.Fatal("the manifest declares no snapper backend")
	}
	return compat.ProbeWith(context.Background(), backend,
		func(context.Context, []string) (string, error) { return versionOutput, nil })
}

// The embedded manifest is the source the header reads, so it has to parse and
// describe the backend the tool actually drives.
func TestEmbeddedManifestDeclaresSnapper(t *testing.T) {
	m, err := manifest.Load(tuisnapper.ManifestJSON)
	if err != nil {
		t.Fatalf("the embedded tool.json does not parse: %v", err)
	}
	if m.Name != toolName {
		t.Errorf("manifest name = %q, want %q", m.Name, toolName)
	}
	backend, ok := m.Backend("snapper")
	if !ok {
		t.Fatal("no snapper backend in the manifest")
	}
	if len(backend.VersionCommand) == 0 || backend.Minimum == "" {
		t.Errorf("snapper backend is incomplete: %+v", backend)
	}
}

// --demo must never probe the host: the version on screen would describe a
// machine the demo is not driving.
func TestProbeCompatSkipsDemo(t *testing.T) {
	got := probeCompat(context.Background(), "snapper", true)
	if got.Backend != "" || got.Version != "" {
		t.Errorf("demo probe = %+v, want the zero result", got)
	}
}

// A backend the manifest does not describe is not an error, it is simply
// nothing to show.
func TestProbeCompatUnknownBackend(t *testing.T) {
	got := probeCompat(context.Background(), "demo", false)
	if got.Backend != "" {
		t.Errorf("unknown backend = %+v, want the zero result", got)
	}
}

// The whole point of the feature: the version reaches the header.
func TestHeaderShowsTheBackendVersion(t *testing.T) {
	a, _ := newTestApp(t)
	if got := a.View(); !strings.Contains(got, "snapper 0.13.1") {
		t.Errorf("the header should carry the probed version, got:\n%s", got)
	}

	a.backendCompat = testCompat(t, "snapper 0.14.0")
	if got := a.View(); !strings.Contains(got, "snapper 0.14.0 (untested)") {
		t.Errorf("an untested version should say so, got:\n%s", got)
	}

	a.backendCompat = testCompat(t, "snapper 0.8.5")
	if got := a.View(); !strings.Contains(got, "below minimum 0.8.6") {
		t.Errorf("a version below the minimum should say so, got:\n%s", got)
	}
}

// A tool that could not read the version still renders a header, without a
// backend fact in it.
func TestHeaderWithoutAProbe(t *testing.T) {
	a, _ := newTestApp(t)
	a.backendCompat = compat.Result{}
	_ = theme.TokyoNight()
	if got := a.View(); strings.Contains(got, "backend:") {
		t.Errorf("no probe means no backend fact, got:\n%s", got)
	}
}

// undochange of a top-level path is a snapper 0.10.6 fix. The confirm dialog
// must warn on an older machine, and stay quiet everywhere else.
func TestUndochangeWarnsOnOldSnapper(t *testing.T) {
	spec, ok := snapper.Spec(snapper.UndoChange)
	if !ok {
		t.Fatal("no undochange spec")
	}
	rootPath := &pending{spec: spec, req: snapper.Request{Files: []string{"/initrd.img"}}}
	deepPath := &pending{spec: spec, req: snapper.Request{Files: []string{"/etc/hosts"}}}

	a, _ := newTestApp(t)
	if body := a.confirmBody(rootPath); strings.Contains(body, "0.10.6") {
		t.Errorf("0.13.1 has the fix, so no warning is due:\n%s", body)
	}

	a.backendCompat = testCompat(t, "snapper 0.10.5")
	if body := a.confirmBody(rootPath); !strings.Contains(body, "0.10.6") {
		t.Errorf("0.10.5 cannot recreate a top-level path:\n%s", body)
	}
	if body := a.confirmBody(deepPath); strings.Contains(body, "0.10.6") {
		t.Errorf("a nested path is unaffected, so no warning is due:\n%s", body)
	}

	// A version nobody could read must not invent a warning either.
	a.backendCompat = compat.Result{}
	if body := a.confirmBody(rootPath); strings.Contains(body, "0.10.6") {
		t.Errorf("an unknown version assumes the capability:\n%s", body)
	}
}
