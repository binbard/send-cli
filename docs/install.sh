#!/bin/sh
set -eu

fail() {
  printf 'send installer: %s\n' "$*" >&2
  exit 1
}

command -v curl >/dev/null 2>&1 || fail 'curl is required'
command -v tar >/dev/null 2>&1 || fail 'tar is required'

case "$(uname -s)" in
  Darwin) platform=darwin ;;
  Linux) platform=linux ;;
  *) fail 'unsupported operating system' ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail 'unsupported CPU architecture' ;;
esac

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
  [ "$platform" = linux ] && [ -n "$termux_prefix" ] || fail 'invalid Termux environment'
  install_dir="$termux_prefix/bin"
else
  install_dir="$HOME/.local/bin"
fi
binary="$install_dir/send"
state_dir="$HOME/.local/share/send-cli-installer"
marker="$state_dir/binary-path"

if [ -f "$marker" ]; then
  IFS= read -r recorded_binary < "$marker" || fail 'invalid installation marker'
  [ "$recorded_binary" = "$binary" ] || fail 'installation marker points to a different binary'
elif [ -e "$binary" ] || [ -L "$binary" ]; then
  fail "$binary already exists and was not installed by this script"
fi

latest_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' 'https://github.com/binbard/send-cli/releases/latest') || fail 'could not find the latest release'
tag=${latest_url##*/}
printf '%s\n' "$tag" | grep -Eq '^v[0-9][A-Za-z0-9.+-]*$' || fail 'no versioned release is published yet'
version=${tag#v}
archive="send-$version-$platform-$arch.tar.gz"
release_url="https://github.com/binbard/send-cli/releases/download/$tag"

temp_root=${TMPDIR:-/tmp}
if [ ! -d "$temp_root" ] || [ ! -w "$temp_root" ]; then temp_root=$HOME; fi
tmp_dir=$(mktemp -d "$temp_root/send-install.XXXXXXXX") || fail 'could not create a temporary directory'
cleanup() {
  rm -f "$tmp_dir/archive" "$tmp_dir/checksums" "$tmp_dir/send" "$tmp_dir/profile"
  rmdir "$tmp_dir" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

curl -fsSL "$release_url/$archive" -o "$tmp_dir/archive" || fail "could not download $archive"
curl -fsSL "$release_url/checksums.txt" -o "$tmp_dir/checksums" || fail 'could not download checksums.txt'
expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$tmp_dir/checksums")
printf '%s\n' "$expected" | grep -Eq '^[a-fA-F0-9]{64}$' || fail "missing or invalid checksum for $archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp_dir/archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp_dir/archive" | awk '{ print $1 }')
else
  fail 'sha256sum or shasum is required'
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $archive"

contents=$(tar -tzf "$tmp_dir/archive") || fail 'invalid release archive'
[ "$contents" = send ] || fail 'release archive must contain only send'
tar -xOzf "$tmp_dir/archive" send > "$tmp_dir/send" || fail 'could not extract send'
chmod 755 "$tmp_dir/send"

mkdir -p "$install_dir" "$state_dir"
staged="$install_dir/.send-install-$$"
trap 'rm -f "$staged"; cleanup' EXIT
cp "$tmp_dir/send" "$staged"
chmod 755 "$staged"
mv -f "$staged" "$binary"
printf '%s\n' "$binary" > "$marker"

if [ "$install_dir" = "$HOME/.local/bin" ]; then
  case ":$PATH:" in
    *":$install_dir:"*) ;;
    *)
      case "${SHELL##*/}" in
        zsh) profile_name=.zprofile ;;
        bash)
          if [ "$platform" = darwin ]; then profile_name=.bash_profile; else profile_name=.bashrc; fi ;;
        *) profile_name=.profile ;;
      esac
      profile="$HOME/$profile_name"
      if [ ! -f "$state_dir/profile-name" ]; then
        if [ -e "$profile" ] && [ ! -f "$profile" ]; then fail "$profile is not a regular file"; fi
        printf '\n# send-cli installer PATH start\nexport PATH="$HOME/.local/bin:$PATH"\n# send-cli installer PATH end\n' >> "$profile"
        printf '%s\n' "$profile_name" > "$state_dir/profile-name"
        printf 'Added %s to PATH in %s. Open a new terminal to use send.\n' "$install_dir" "$profile"
      fi
      ;;
  esac
fi

printf 'Installed send %s to %s\n' "$version" "$binary"
printf 'Run this script again to update.\n'
