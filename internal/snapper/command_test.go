package snapper

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// Two things are worth asserting above everything else, and both are here:
//
//   - the command a key produces is exactly the command the preview shows;
//   - nothing runs that the user did not confirm.
//
// Everything else in this file exists so that a wrong argv fails a test
// rather than a filesystem. Every expected command line below was checked
// against snapper 0.13.1's own usage output.

// request is a small helper for the table below.
func request(config string, numbers []int, files []string, values map[FieldKind]string) Request {
	if values == nil {
		values = map[FieldKind]string{}
	}
	return Request{Config: config, Numbers: numbers, Files: files, Values: values}
}

func TestBuildCommand(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		req      Request
		wantArgv []string
		wantErr  string
	}{
		{
			name: "create with a description and a cleanup algorithm",
			key:  "c",
			req: request("root", nil, nil, map[FieldKind]string{
				FieldDescription: "before the nvidia downgrade",
				FieldType:        "single",
				FieldCleanup:     "number",
			}),
			wantArgv: []string{"snapper", "-c", "root", "create",
				"--description", "before the nvidia downgrade",
				"--cleanup-algorithm", "number"},
		},
		{
			name: "create a pre snapshot",
			key:  "c",
			req: request("root", nil, nil, map[FieldKind]string{
				FieldDescription: "pacman -Syu",
				FieldType:        "pre",
				FieldCleanup:     "number",
			}),
			wantArgv: []string{"snapper", "-c", "root", "create",
				"--type", "pre", "--description", "pacman -Syu",
				"--cleanup-algorithm", "number"},
		},
		{
			name: "the none option leaves the algorithm off the command line",
			key:  "c",
			req: request("root", nil, nil, map[FieldKind]string{
				FieldDescription: "keep this one",
				FieldType:        "single",
				FieldCleanup:     "(none, keep forever)",
			}),
			wantArgv: []string{"snapper", "-c", "root", "create",
				"--description", "keep this one"},
		},
		{
			name:     "create with nothing filled in is still a valid command",
			key:      "c",
			req:      request("root", nil, nil, nil),
			wantArgv: []string{"snapper", "-c", "root", "create"},
		},
		{
			name:     "delete one snapshot",
			key:      "D",
			req:      request("root", []int{42}, nil, nil),
			wantArgv: []string{"snapper", "-c", "root", "delete", "42"},
		},
		{
			name: "delete several, listed in ascending order",
			key:  "D",
			req:  request("root", []int{47, 31, 42}, nil, nil),
			wantArgv: []string{"snapper", "-c", "root", "delete",
				"31", "42", "47"},
		},
		{
			name:     "delete de-duplicates a number marked twice",
			key:      "D",
			req:      request("root", []int{42, 42}, nil, nil),
			wantArgv: []string{"snapper", "-c", "root", "delete", "42"},
		},
		{
			name: "modify the description",
			key:  "e",
			req: request("root", []int{42}, nil,
				map[FieldKind]string{FieldDescription: "known good"}),
			wantArgv: []string{"snapper", "-c", "root", "modify",
				"--description", "known good", "42"},
		},
		{
			name: "an empty description clears the label rather than being dropped",
			key:  "e",
			req: request("root", []int{42}, nil,
				map[FieldKind]string{FieldDescription: ""}),
			wantArgv: []string{"snapper", "-c", "root", "modify",
				"--description", "", "42"},
		},
		{
			name: "pin a snapshot by clearing its cleanup algorithm",
			key:  "a",
			req: request("root", []int{42}, nil,
				map[FieldKind]string{FieldCleanup: "(none, keep forever)"}),
			wantArgv: []string{"snapper", "-c", "root", "modify",
				"--cleanup-algorithm", "", "42"},
		},
		{
			name: "run a cleanup",
			key:  "C",
			req: request("root", nil, nil,
				map[FieldKind]string{FieldAlgorithm: "empty-pre-post"}),
			wantArgv: []string{"snapper", "-c", "root", "cleanup", "empty-pre-post"},
		},
		{
			name: "undo one file",
			key:  "u",
			req: request("root", []int{46, 47},
				[]string{"/etc/pacman.d/mirrorlist"}, nil),
			wantArgv: []string{"snapper", "-c", "root", "undochange", "46..47",
				"/etc/pacman.d/mirrorlist"},
		},
		{
			name: "undo several files, in the order the diff listed them",
			key:  "u",
			req: request("root", []int{46, 47},
				[]string{"/etc/mkinitcpio.conf", "/etc/pacman.d/mirrorlist"}, nil),
			wantArgv: []string{"snapper", "-c", "root", "undochange", "46..47",
				"/etc/mkinitcpio.conf", "/etc/pacman.d/mirrorlist"},
		},
		{
			name: "undo against the live subvolume",
			key:  "u",
			req:  request("root", []int{42, 0}, []string{"/etc/fstab"}, nil),
			wantArgv: []string{"snapper", "-c", "root", "undochange", "42..0",
				"/etc/fstab"},
		},
		{
			name:     "rollback",
			key:      "R",
			req:      request("root", []int{42}, nil, nil),
			wantArgv: []string{"snapper", "-c", "root", "rollback", "42"},
		},

		// The refusals. Each of these would otherwise reach snapper as a
		// command the user had already confirmed.
		{
			name: "no config", key: "D",
			req: request("", []int{42}, nil, nil), wantErr: "no config",
		},
		{
			name: "no snapshot selected", key: "D",
			req: request("root", nil, nil, nil), wantErr: "no snapshot selected",
		},
		{
			name: "snapshot 0 is the live subvolume, not a snapshot", key: "D",
			req: request("root", []int{0}, nil, nil), wantErr: "live subvolume",
		},
		{
			name: "modify takes exactly one snapshot", key: "e",
			req: request("root", []int{1, 2}, nil,
				map[FieldKind]string{FieldDescription: "x"}),
			wantErr: "one snapshot at a time",
		},
		{
			name: "an unknown cleanup algorithm is refused", key: "C",
			req: request("root", nil, nil,
				map[FieldKind]string{FieldAlgorithm: "everything"}),
			wantErr: "unknown cleanup algorithm",
		},
		{
			name: "undo needs a file", key: "u",
			req: request("root", []int{1, 2}, nil, nil), wantErr: "no file selected",
		},
		{
			name: "undo needs two different snapshots", key: "u",
			req:     request("root", []int{2, 2}, []string{"/etc/fstab"}, nil),
			wantErr: "two different snapshots",
		},
		{
			name: "a range needs exactly two snapshots", key: "u",
			req:     request("root", []int{1, 2, 3}, []string{"/etc/fstab"}, nil),
			wantErr: "two snapshots",
		},
		{
			name: "a post snapshot cannot be created on its own", key: "c",
			req: request("root", nil, nil,
				map[FieldKind]string{FieldType: "post"}),
			wantErr: "single or pre",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, ok := ActionFor(tc.key)
			if !ok {
				t.Fatalf("no action bound to %q", tc.key)
			}
			cmd, err := BuildCommand(spec, tc.req)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error, got %q", cmd)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			if !reflect.DeepEqual(cmd.Argv, tc.wantArgv) {
				t.Errorf("Argv =\n  %q\nwant\n  %q", cmd.Argv, tc.wantArgv)
			}
			if cmd.Description == "" {
				t.Error("a command needs a description for the dialog title")
			}
		})
	}
}

func TestDeleteDialogNamesEveryNumber(t *testing.T) {
	// The confirm dialog is the only thing standing between the user and a
	// deletion, so it has to say which snapshots go.
	spec, _ := ActionFor("D")
	cmd, err := BuildCommand(spec, request("root", []int{47, 31, 42}, nil, nil))
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	for _, want := range []string{"31", "42", "47"} {
		if !strings.Contains(cmd.Description, want) {
			t.Errorf("the dialog title %q does not name snapshot %s",
				cmd.Description, want)
		}
	}
	if !cmd.Destructive {
		t.Error("a delete must be marked destructive so the dialog warns")
	}
}

func TestReadArgs(t *testing.T) {
	// These are the reads, checked against snapper's own usage. `status` and
	// `undochange` take an N..M range; `diff` takes the range and then the
	// paths, with no `--` separator — passing one makes snapper look for a
	// file literally called "--".
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"list-configs", ListConfigsArgs(),
			[]string{"snapper", "--jsonout", "list-configs"}},
		{"list", ListArgs("root"),
			[]string{"snapper", "--jsonout", "-c", "root", "list"}},
		{"status", StatusArgs("root", 46, 47),
			[]string{"snapper", "-c", "root", "status", "46..47"}},
		{"diff", DiffArgs("root", 46, 47, "/etc/fstab"),
			[]string{"snapper", "-c", "root", "diff", "46..47", "/etc/fstab"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
	for _, argv := range [][]string{DiffArgs("root", 1, 2, "/x"), StatusArgs("root", 1, 2)} {
		for _, arg := range argv {
			if arg == "--" {
				t.Errorf("%q carries a -- separator snapper does not accept", argv)
			}
		}
	}
}

func TestActionTableIsCoherent(t *testing.T) {
	// A duplicate binding would silently shadow an action in the key handler,
	// and an action with no label or body would open a dialog that explains
	// nothing.
	seen := map[string]Action{}
	for _, spec := range Actions {
		if spec.Key == "" {
			t.Errorf("%q has no key, so it can never be reached", spec.Action)
		}
		if other, ok := seen[spec.Key]; ok {
			t.Errorf("key %q is bound to both %q and %q", spec.Key, other, spec.Action)
		}
		seen[spec.Key] = spec.Action
		if spec.Label == "" || spec.Body == "" {
			t.Errorf("%q needs a label and a body for the confirm dialog", spec.Action)
		}
		if spec.Scope == "" {
			t.Errorf("%q needs a scope so the app knows what it applies to", spec.Action)
		}
		// Every field must be answerable: a picker needs options, a text
		// prompt needs a title.
		for _, field := range spec.Fields {
			if field.Kind == "" || field.Title == "" {
				t.Errorf("%q has a field with no kind or title", spec.Action)
			}
		}
		if found, ok := Spec(spec.Action); !ok || found.Key != spec.Key {
			t.Errorf("Spec(%q) did not return the table's own entry", spec.Action)
		}
	}
	// Every action that can lose data must be marked, or its dialog will not
	// be painted as a warning.
	for _, action := range []Action{Delete, CleanupNow, UndoChange, RollbackNow} {
		spec, ok := Spec(action)
		if !ok {
			t.Fatalf("%q is missing from the action table", action)
		}
		if !spec.Destructive {
			t.Errorf("%q loses state and must be marked destructive", action)
		}
	}
}

func TestFakePreviewMatchesWhatRuns(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	spec, _ := ActionFor("D")
	cmd, err := f.Build(spec, request(DemoConfig, []int{42}, nil, nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	preview := f.Preview(cmd)
	if preview != "snapper -c root delete 42" {
		t.Errorf("preview = %q", preview)
	}

	if _, err := f.Run(ctx, cmd); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.Commands()) != 1 {
		t.Fatalf("ran %d commands, want 1", len(f.Commands()))
	}
	// The command that ran must be the one the preview showed, character for
	// character. This is the guarantee the whole family is built around.
	if got := f.Preview(f.Commands()[0]); got != preview {
		t.Errorf("ran %q, but the preview promised %q", got, preview)
	}
}

func TestFakeRunsNothingUntilAsked(t *testing.T) {
	// Building and previewing a command must not touch anything: the confirm
	// dialog is the only path to a change.
	f := NewFake()
	for _, key := range []string{"c", "D", "e", "a", "C"} {
		spec, _ := ActionFor(key)
		_, _ = f.Build(spec, request(DemoConfig, []int{42}, nil,
			map[FieldKind]string{
				FieldDescription: "x", FieldType: "single",
				FieldCleanup: "number", FieldAlgorithm: "number",
			}))
	}
	if len(f.Commands()) != 0 {
		t.Errorf("building previews ran %d commands, want none", len(f.Commands()))
	}
}

func TestFakeAppliesTheChange(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	before, err := f.Snapshots(ctx, DemoConfig)
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}

	spec, _ := ActionFor("D")
	cmd, _ := f.Build(spec, request(DemoConfig, []int{42}, nil, nil))
	if _, err := f.Run(ctx, cmd); err != nil {
		t.Fatalf("Run: %v", err)
	}

	after, err := f.Snapshots(ctx, DemoConfig)
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if len(after) != len(before)-1 {
		t.Fatalf("after the delete there are %d rows, want %d", len(after), len(before)-1)
	}
	for _, s := range after {
		if s.Number == 42 {
			t.Error("snapshot 42 should be gone")
		}
	}

	// Creating one puts it back, with the values the dialog collected.
	create, _ := ActionFor("c")
	cmd, err = f.Build(create, request(DemoConfig, nil, nil, map[FieldKind]string{
		FieldDescription: "a fresh one", FieldType: "single", FieldCleanup: "number",
	}))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := f.Run(ctx, cmd); err != nil {
		t.Fatalf("Run: %v", err)
	}
	created, err := f.Snapshots(ctx, DemoConfig)
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	newest, ok := Newest(created)
	if !ok || newest.Description != "a fresh one" || newest.Cleanup != "number" {
		t.Errorf("newest = %+v, want the snapshot the dialog described", newest)
	}
}

func TestFakeRefusesAnUnknownConfig(t *testing.T) {
	f := NewFake()
	if _, err := f.Snapshots(context.Background(), "nope"); err == nil {
		t.Error("expected snapper's own not-found error")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want the wording snapper uses", err)
	}
}

func TestFakeStatusAndDiff(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	changes, err := f.Status(ctx, DemoConfig, 46, 47)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("the demo comparison should not be empty")
	}
	kinds := map[ChangeKind]bool{}
	for _, change := range changes {
		kinds[change.Kind] = true
		if change.Path == "" || change.Status == "" {
			t.Errorf("change %+v is missing a path or its flags", change)
		}
	}
	// A demo that only ever shows modified files would not exercise the view.
	for _, kind := range []ChangeKind{Created, Deleted, Modified} {
		if !kinds[kind] {
			t.Errorf("the demo comparison has no %s file", kind)
		}
	}

	if _, err := f.Status(ctx, DemoConfig, 47, 47); err == nil {
		t.Error("comparing a snapshot with itself should be refused")
	}

	text, err := f.Diff(ctx, DemoConfig, 46, 47, changes[0].Path)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(text, changes[0].Path) {
		t.Errorf("the diff should name the path it is for, got %q", text)
	}
	if _, err := f.Diff(ctx, DemoConfig, 46, 47, ""); err == nil {
		t.Error("a diff with no path should be refused")
	}
}

func TestFakePlatform(t *testing.T) {
	f := NewFake()
	root := f.Platform(context.Background(), Config{Name: "root", Subvolume: "/"})
	if root.Kind != RollbackBootMenu || len(root.Entries) == 0 {
		t.Errorf("the demo should show the boot-menu layout, got %+v", root.Kind)
	}
	for _, entry := range root.Entries {
		if entry.Number == 0 || entry.Title == "" {
			t.Errorf("boot entry %+v is missing its number or title", entry)
		}
	}
	home := f.Platform(context.Background(), Config{Name: "home", Subvolume: "/home"})
	if home.Kind != RollbackUnsupported {
		t.Errorf("a non-root config cannot roll back, got %q", home.Kind)
	}
	if home.Reason == "" {
		t.Error("every platform answer needs a reason the user can read")
	}
}
