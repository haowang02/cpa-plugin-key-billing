#!/bin/sh

set -eu

repository="haowang02/cpa-plugin-key-billing"
plugin_name="cpa-key-billing"
plugin_dir="$(pwd)/plugins"
tmp_dir=""
staged_file=""

fail() {
  printf 'cpa-key-billing: %s\n' "$*" >&2
  exit 1
}

checksum_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    fail "sha256sum or shasum is required to verify the download"
  fi
}

cleanup() {
  if [ -n "$staged_file" ]; then
    rm -f "$staged_file"
  fi
  if [ -n "$tmp_dir" ]; then
    rm -rf "$tmp_dir"
  fi
}

trap cleanup EXIT
trap 'exit 1' HUP INT TERM

command -v curl >/dev/null 2>&1 || fail "curl is required to download the plugin"
command -v tar >/dev/null 2>&1 || fail "tar is required to extract the plugin"

case "$(uname -s)" in
  Darwin)
    target_os="darwin"
    extension="dylib"
    ;;
  Linux)
    target_os="linux"
    extension="so"
    ;;
  *)
    fail "unsupported operating system: $(uname -s)"
    ;;
esac

case "$(uname -m)" in
  x86_64 | amd64)
    target_arch="amd64"
    ;;
  arm64 | aarch64)
    target_arch="arm64"
    ;;
  *)
    fail "unsupported architecture: $(uname -m)"
    ;;
esac

latest_release_url="$(
  curl -fsSL --retry 3 --connect-timeout 15 -o /dev/null -w '%{url_effective}' \
    "https://github.com/${repository}/releases/latest"
)" || fail "failed to resolve the latest release"
release_tag="${latest_release_url##*/}"
version="$(
  printf '%s\n' "$release_tag" \
    | awk '/^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$/ {print substr($0, 2)}'
)"
[ -n "$version" ] || fail "latest release has an invalid tag: ${release_tag}"

asset="${plugin_name}_${version}_${target_os}_${target_arch}.tar.gz"
plugin_file="${plugin_name}.${extension}"
download_url="https://github.com/${repository}/releases/download/${release_tag}/${asset}"
checksums_url="https://github.com/${repository}/releases/download/${release_tag}/checksums.txt"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/${plugin_name}.XXXXXX")" || fail "failed to create temporary directory"
archive="${tmp_dir}/${asset}"
checksums="${tmp_dir}/checksums.txt"

printf 'Downloading %s...\n' "$asset"
curl -fsSL --retry 3 --connect-timeout 15 -o "$archive" "$download_url" \
  || fail "failed to download: ${download_url}"

curl -fsSL --retry 3 --connect-timeout 15 -o "$checksums" "$checksums_url" \
  || fail "failed to download checksums: ${checksums_url}"

expected_checksum="$(awk -v name="$asset" '$2 == name || $2 == "*" name {print $1; exit}' "$checksums")"
[ -n "$expected_checksum" ] || fail "checksums file does not contain ${asset}"
actual_checksum="$(checksum_file "$archive")"
[ "$actual_checksum" = "$expected_checksum" ] \
  || fail "download checksum mismatch"

tar -xzf "$archive" -C "$tmp_dir" "$plugin_file" \
  || fail "release archive does not contain ${plugin_file}"
[ -s "${tmp_dir}/${plugin_file}" ] || fail "release archive contains an empty ${plugin_file}"

mkdir -p "$plugin_dir" || fail "failed to create ${plugin_dir}"
staged_file="${plugin_dir}/.${plugin_file}.tmp.$$"
cp "${tmp_dir}/${plugin_file}" "$staged_file" || fail "failed to write to ${plugin_dir}"
chmod 0755 "$staged_file"
mv -f "$staged_file" "${plugin_dir}/${plugin_file}"
staged_file=""

printf 'Installed: %s\n' "${plugin_dir}/${plugin_file}"
printf 'Restart CLIProxyAPI to load the plugin.\n'
