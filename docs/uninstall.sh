#!/bin/sh
set -eu

fail() {
  printf 'send uninstaller: %s\n' "$*" >&2
  exit 1
}

state_dir="$HOME/.local/share/send-cli-installer"
marker="$state_dir/binary-path"
[ -f "$marker" ] || fail 'no script-managed installation was found'
IFS= read -r binary < "$marker" || fail 'invalid installation marker'

termux_prefix=${TERMUX__PREFIX:-${PREFIX:-}}
if [ -z "$termux_prefix" ]; then
  case "$HOME" in */com.termux/files/home) termux_prefix=${HOME%/home}/usr ;; esac
fi
if [ -z "$termux_prefix" ]; then
  shell_path=$(command -v sh)
  case "$shell_path" in */com.termux/files/usr/bin/sh) termux_prefix=${shell_path%/bin/sh} ;; esac
fi
case "$termux_prefix" in */com.termux/files/usr) termux_detected=1 ;; *) termux_detected=0 ;; esac
if [ -n "${TERMUX_VERSION:-}${TERMUX__PREFIX:-}" ] || [ "$termux_detected" = 1 ]; then
  [ -n "$termux_prefix" ] || fail 'invalid Termux environment'
  expected_binary="$termux_prefix/bin/send"
else
  expected_binary="$HOME/.local/bin/send"
fi
[ "$binary" = "$expected_binary" ] || fail 'installation marker points outside the expected location'

profile_marker="$state_dir/profile-name"
if [ -f "$profile_marker" ]; then
  IFS= read -r profile_name < "$profile_marker" || fail 'invalid profile marker'
  case "$profile_name" in
    .zprofile|.bash_profile|.bashrc|.profile) ;;
    *) fail 'invalid profile marker' ;;
  esac
  profile="$HOME/$profile_name"
  if [ -f "$profile" ]; then
    start_count=$(grep -c '^# send-cli installer PATH start$' "$profile" || true)
    end_count=$(grep -c '^# send-cli installer PATH end$' "$profile" || true)
    [ "$start_count" = 1 ] && [ "$end_count" = 1 ] || fail 'PATH markers in the shell profile were changed; remove them manually'
    start_line=$(grep -n '^# send-cli installer PATH start$' "$profile" | cut -d: -f1)
    end_line=$(grep -n '^# send-cli installer PATH end$' "$profile" | cut -d: -f1)
    [ "$start_line" -lt "$end_line" ] || fail 'PATH markers in the shell profile are out of order'
    tmp_profile=$(mktemp "$HOME/.send-profile.XXXXXX") || fail 'could not create a temporary profile'
    trap 'rm -f "$tmp_profile"' EXIT
    sed '/^# send-cli installer PATH start$/,/^# send-cli installer PATH end$/d' "$profile" > "$tmp_profile"
    cat "$tmp_profile" > "$profile"
    rm -f "$tmp_profile"
    trap - EXIT
  fi
  rm -f "$profile_marker"
fi

rm -f "$binary" "$marker"
rmdir "$state_dir" 2>/dev/null || true
printf 'Uninstalled send from %s. Provider settings were kept.\n' "$binary"
