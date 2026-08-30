package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-snapper/internal/snapper"
)

// decodeCheck runs --check against the demo backend and returns the report.
func decodeCheck(t *testing.T, wanted string) checkReport {
	t.Helper()
	var out bytes.Buffer
	if err := runCheck(snapper.NewFake(), wanted, compat.Result{}, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	var report checkReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("the report is not valid JSON: %v\n%s", err, out.String())
	}
	return report
}

func TestRunCheckReportsTheParsedModel(t *testing.T) {
	report := decodeCheck(t, "")

	if report.Tool != toolName {
		t.Errorf("tool = %q, want %q", report.Tool, toolName)
	}
	if report.Backend != "demo" {
		t.Errorf("backend = %q, want demo", report.Backend)
	}
	// An empty -config opens on the first config the backend reports, which
	// is what the UI does, so the two never disagree.
	if report.Config == "" || report.Config != report.ConfigList[0].Name {
		t.Errorf("config = %q, want the first of %+v", report.Config, report.ConfigList)
	}
	// The counts are what a smoke test asserts on, so they have to be the
	// real lengths rather than a hard-coded guess: a parser that fetched the
	// output and failed to read it reports zero here.
	if report.Snapshots != len(report.Model) {
		t.Errorf("snapshots = %d, but the model holds %d rows",
			report.Snapshots, len(report.Model))
	}
	if report.Real >= report.Snapshots {
		t.Errorf("real_snapshots = %d must exclude snapshot 0 out of %d",
			report.Real, report.Snapshots)
	}
}

func TestRunCheckReportsThePlatform(t *testing.T) {
	report := decodeCheck(t, "")

	// The platform verdict is the reason --check exists for this tool: it is
	// the one answer that differs between an openSUSE box and an Omarchy one,
	// and a lab run on a limine machine must read "boot-menu" here.
	if report.Rollback != string(snapper.RollbackBootMenu) {
		t.Errorf("rollback = %q, want %q", report.Rollback, snapper.RollbackBootMenu)
	}
	if report.Reason == "" {
		t.Error("the rollback verdict came with no reason")
	}
	if len(report.BootEntries) == 0 {
		t.Error("a boot-menu verdict with no boot entries")
	}
	for _, entry := range report.BootEntries {
		if entry.Number == 0 {
			t.Errorf("boot entry %q lost its snapshot number", entry.Title)
		}
	}
	if len(report.Timers) != len(snapper.TimerUnits) {
		t.Errorf("got %d timers, want %d", len(report.Timers), len(snapper.TimerUnits))
	}
}

func TestRunCheckRejectsAnUnknownConfig(t *testing.T) {
	var out bytes.Buffer
	err := runCheck(snapper.NewFake(), "not-a-config", compat.Result{}, &out)
	if err == nil {
		t.Fatal("an unknown config was accepted")
	}
	// The message has to name the config, because "not found" alone reads as
	// snapper being missing rather than the -config flag being wrong.
	if !strings.Contains(err.Error(), "not-a-config") {
		t.Errorf("the error does not name the config: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("a failed check still printed a report: %s", out.String())
	}
}
