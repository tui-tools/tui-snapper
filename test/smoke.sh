#!/bin/bash
# Backend smoke test for tui-snapper, run inside a lab guest.
#
# The contract (see tui-tools/tui-lab): this script runs on the guest as the
# unprivileged lab user, escalates with `sudo -n` only, prints a short PASS/FAIL
# table and exits non-zero if anything failed. The binary under test is at
# $TUI_LAB_BIN (default: tui-snapper on PATH).
#
# What it proves is that the tool reads the machine's *real* snapper and agrees
# with snapper's own output — not that a fake renders. The lab already covers
# --version and a --demo frame; this covers the backend.
#
# Every mutation here goes through `snapper` itself, never through the tool:
# the assertion is always "tui-snapper reports what snapper did". The one
# snapshot it creates is deleted again at the end, and on a limine machine
# `limine-snapper-sync` is re-run afterwards so the boot menu is left as found.
set -uo pipefail

bin="${TUI_LAB_BIN:-tui-snapper}"
# TOOL is the manifest name, which is what a compatibility result is keyed on.
TOOL=tui-snapper
pass=0
fail=0

# check runs one assertion. It takes a label, a command and a grep pattern the
# command's output must match. Output is captured so a failure can show it.
check() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# --- compatibility evidence -------------------------------------------------
#
# The manifest's `tested` list is generated, not claimed: it is rebuilt from
# compat/results.jsonl by tui-kit/tools/compat-sync.py, and this is where a
# line of that file comes from. The version recorded is the one the tool itself
# probed, read back out of --check, so it describes the machine that really ran
# the suite rather than what the tester assumed was installed.
#
# The line is printed behind a `compat-result:` prefix so it survives the trip
# out of the guest through the lab's per-VM log, and appended to
# $TUI_COMPAT_RESULTS as well for a run outside the lab.
record_compat() {
  local report="$1" outcome="$2" backend version distro today block
  block=$(sed -n '/"compat": {/,/^  }/p' <<<"$report")
  backend=$(sed -n 's/.*"backend": "\([^"]*\)".*/\1/p' <<<"$block" | head -1)
  version=$(sed -n 's/.*"version": "\([^"]*\)".*/\1/p' <<<"$block" | head -1)
  if [[ -z $backend || -z $version ]]; then
    echo "      no version was probed, so no compatibility result is recorded"
    return
  fi

  distro=$(. /etc/os-release && echo "${ID}-${VERSION_ID:-rolling}")
  today=$(date -u +%Y-%m-%d)
  local line
  line=$(printf '{"backend":"%s","date":"%s","distro":"%s","result":"%s","suite":"smoke","tool":"%s","version":"%s"}' \
    "$backend" "$today" "$distro" "$outcome" "$TOOL" "$version")

  printf 'compat-result: %s\n' "$line"
  if [[ -n ${TUI_COMPAT_RESULTS:-} ]]; then
    printf '%s\n' "$line" >>"$TUI_COMPAT_RESULTS"
  fi
}

echo "--- tui-snapper smoke on $(. /etc/os-release && echo "$PRETTY_NAME")"

if ! command -v snapper >/dev/null; then
  echo "FAIL  snapper is not installed on this machine"
  exit 1
fi

# Which config to drive. `root` is the interesting one — it is the only config
# a rollback can ever apply to — and the lab's Ubuntu and Fedora guests have no
# btrfs root, so they get `data` on the spare /dev/vdb instead. Whichever is
# there, the tool must open on the same one, so the name is passed explicitly.
if sudo -n snapper list-configs | grep -qE '^root '; then
  config=root
elif sudo -n snapper list-configs | grep -qE '^data '; then
  config=data
else
  echo "FAIL  no snapper config on this machine"
  sudo -n snapper list-configs 2>&1 | sed 's/^/      | /'
  exit 1
fi
subvolume=$(sudo -n snapper list-configs | awk -v c="$config" '$1 == c {print $3}')
echo "      config=$config subvolume=$subvolume"

# limine-snapper-sync is what turns a snapshot into a boot menu entry, and its
# presence is what decides which rollback mechanism this machine has.
has_limine=false
if command -v limine-snapper-sync >/dev/null && [[ "$subvolume" == "/" ]]; then
  has_limine=true
fi
echo "      limine-snapper-sync=$has_limine"

# --- the report block ------------------------------------------------------
#
# --report is read-only and unprivileged, so it is smoked without sudo — which
# matters more here than anywhere else in the family, because everything else
# this tool does needs root: a user who cannot escalate is exactly the one who
# most needs to be able to file a usable bug. What is asserted is that it names
# the backend this machine drives, that it still answers under --demo, and that
# it keeps its privacy promise — the block goes into a public issue, so a home
# path or the host name appearing in it is a bug, not a cosmetic detail.
check "report names the selected backend" \
  "$bin --report" \
  '^backend: snapper'

check "report says the run was live" \
  "$bin --report" \
  '^mode: live$'

check "report works in demo mode too" \
  "$bin --demo --report" \
  '^backend: demo$'

check "and says so on the mode line" \
  "$bin --demo --report" \
  '^mode: demo'

# The distro and kernel lines are excluded from the host-name search rather
# than from the promise: they are built from /etc/os-release and from uname's
# release and machine fields, never from its nodename, and on a guest called
# "fedora" or "ubuntu" — which is most of them — the host name is a substring
# of the distribution's own. Everything else in the block is searched.
check "report leaks neither a home path nor the host name" \
  "$bin --report | grep -vE '^(distro|kernel): ' | grep -cE '/home/|$(uname -n)' || true" \
  '^0$'

# A snapshot of our own, so the machine has something to read whatever state
# it booted in, and so the description below is one nothing else could have
# written. It is deleted again at the end.
marker="tui-snapper lab smoke $$"
created=$(sudo -n snapper -c "$config" create -p -d "$marker")
if [[ -z "$created" ]]; then
  echo "FAIL  snapper create printed no snapshot number"
  exit 1
fi
echo "      created snapshot $created"

# cleanup removes the snapshot again and puts the boot menu back, whatever
# happened above, so a failed run does not leave the machine dirty.
cleanup() {
  sudo -n snapper -c "$config" delete "$created" >/dev/null 2>&1
  if [[ "$has_limine" == true ]]; then
    sudo -n limine-snapper-sync >/dev/null 2>&1
  fi
}
trap cleanup EXIT

if [[ "$has_limine" == true ]]; then
  # Normally the snapper plugin triggers this; the lab image has no
  # inotifywait, so it is called by hand to get a deterministic boot menu.
  sudo -n limine-snapper-sync >/dev/null 2>&1
fi

# --check is the tool's non-interactive read path: it drives the real backend
# and prints the parsed model as JSON. Captured once, asserted many times.
report=$("$bin" --check --config "$config" 2>&1)
report_status=$?
if [[ $report_status -ne 0 ]]; then
  echo "FAIL  --check could not read this machine (exit $report_status)"
  sed 's/^/      | /' <<<"$report" | head -20
  exit 1
fi
# report_says asserts against that single capture rather than re-reading, so
# the assertions cannot disagree with each other about the machine's state.
report_says() {
  check "$1" "printf '%s' \"\$report\"" "$2"
}

# 1. The read path works at all and names the backend it drove.
report_says "check reads the snapper backend" '"backend": "snapper"'

# 2. The tool opened on the config it was told to, with snapper's own
#    subvolume for it, rather than silently falling back to the first one.
report_says "the config and its subvolume are read" \
  "\"config\": \"$config\""
report_says "the subvolume matches \`snapper list-configs\` ($subvolume)" \
  "\"subvolume\": \"$subvolume\""

# 3. The config count matches snapper's own. A tool that fetched the output
#    but failed to parse it reports zero.
config_count=$(sudo -n snapper list-configs | tail -n +3 | grep -c .)
report_says "config count matches \`snapper list-configs\` ($config_count)" \
  "\"configs\": $config_count"

# 4. The snapshot count matches snapper's own. snapper's table carries a
#    two-line header and a row 0 for the live subvolume, which the tool
#    reports separately as real_snapshots — so both figures are asserted.
rows=$(sudo -n snapper -c "$config" list | tail -n +3 | grep -c .)
real=$((rows - 1))
report_says "snapshot count matches \`snapper list\` ($rows rows)" \
  "\"snapshots\": $rows"
report_says "the live subvolume is excluded from real_snapshots ($real)" \
  "\"real_snapshots\": $real"

# 5. The snapshot created above is in the parsed model, with its number and
#    its description. This is the parser test that matters: it proves the
#    tool read this machine and not a cached or invented list.
report_says "the snapshot just created is parsed (number $created)" \
  "\"Number\": $created,"
report_says "its description survived the parse" \
  "\"Description\": \"$marker\""

# 6. snapper's feature flags were read from `snapper --version`, which is what
#    the rollback verdict below partly rests on.
report_says "snapper's feature flags are read" '"snapper_flags": \['

# 7. The rollback verdict. This is the one answer that genuinely differs
#    between machines, so it is asserted against what this machine actually
#    has rather than against a constant.
if [[ "$has_limine" == true ]]; then
  report_says "the platform is detected as the limine boot menu" \
    '"rollback": "boot-menu"'
  report_says "the boot entries were read from /boot/limine.conf" \
    '"boot_config": "/boot/limine.conf"'
  # The tool must agree with the file: as many entries as
  # limine-snapper-sync wrote snapshot nodes under the Snapshots subtree.
  entries=$(sudo -n grep -cE '^ *///[^/]' /boot/limine.conf)
  check "boot entry count matches /boot/limine.conf ($entries)" \
    "printf '%s' \"\$report\" | grep -c '\"Title\": '" \
    "^$entries$"
  # And the new snapshot's number has to survive whatever title format this
  # limine-snapper-sync writes, which is the whole reason
  # internal/snapper/limine.go has a title parser at all. Asserted against
  # the boot_entries block alone, so a match in the snapshot list above
  # cannot stand in for it.
  check "the boot menu offers the new snapshot (number $created)" \
    "printf '%s' \"\$report\" | sed -n '/\"boot_entries\"/,/\"snapper_flags\"/p'" \
    "\"Number\": $created,"
else
  # No limine, and the config is not the root filesystem: `snapper rollback`
  # cannot apply here and the tool must say so rather than offering it.
  report_says "a non-root config is reported as not rollback-capable" \
    '"rollback": "unsupported"'
  report_says "and the verdict explains itself" \
    "\"rollback_reason\": \".*root filesystem"
fi

# 7b. The config's own settings, which is what the retention form on the
#     config screen opens on. The form seeds itself from this read, so a
#     mismatch here means it would offer the user the wrong current value —
#     and the assertion is against snapper's own get-config, not a constant.
number_limit=$(sudo -n snapper -c "$config" get-config \
  | awk -F'|' '$1 ~ /^NUMBER_LIMIT[ \t]*$/ {gsub(/ /, "", $2); print $2}')
if [[ -n "$number_limit" ]]; then
  report_says "NUMBER_LIMIT matches \`snapper get-config\` ($number_limit)" \
    "\"NUMBER_LIMIT\": \"$number_limit\""
else
  echo "      snapper get-config printed no NUMBER_LIMIT, so it is not asserted"
fi
report_says "the timeline keys are read too" '"TIMELINE_LIMIT_HOURLY": "'

# 7c. Only the retention keys are reported. A config also names the users and
#     groups allowed to use it, and a --check output gets pasted into issues.
check "the settings block carries no user or group key" \
  "printf '%s' \"\$report\" | sed -n '/\"settings\": {/,/^  }/p' | grep -c 'ALLOW_' || true" \
  '^0$'

# 8. The systemd timers are reported, read-only. Both units are always listed,
#    even on a machine where they are not installed, so the screen can say so.
report_says "both snapper timers are reported" '"Unit": "snapper-timeline.timer"'
report_says "the cleanup timer is reported too" '"Unit": "snapper-cleanup.timer"'

# 9. What `snapper status` sees between the snapshot and the live subvolume is
#    what the tool's diff screen reads. An empty result is a valid answer on a
#    quiet machine, so this asserts the command succeeds, not that it found
#    changes — the parse itself is covered by the captured fixtures.
check "the status read path runs against two real snapshots" \
  "sudo -n snapper -c $config status $created..0 >/dev/null && echo ok" \
  '^ok$'

# 10. The tool refuses a config this machine does not have, instead of
#     reporting an empty machine.
check "an unknown config is refused by name" \
  "$bin --check --config no-such-config; [[ \$? -ne 0 ]]" \
  'no-such-config'

# 11. The startup version probe found this machine's snapper. The version is
#     what the compatibility record below is keyed on, so an empty probe is a
#     failure worth naming rather than a silently missing line.
snapper_version=$(snapper --version | sed -n 's/^snapper \([0-9][0-9.]*\).*/\1/p' | head -1)
check "the probed version matches \`snapper --version\` ($snapper_version)" \
  "printf '%s' \"\$report\" | sed -n '/\"compat\": {/,/^  }/p'" \
  "\"version\": \"$snapper_version\""

# The line the lab carries back out, and `make compat` turns into the manifest's
# tested list. The report is the same capture every assertion above used.
if [[ $fail -eq 0 ]]; then
  record_compat "$report" pass
else
  record_compat "$report" fail
fi

echo "--- tui-snapper: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
