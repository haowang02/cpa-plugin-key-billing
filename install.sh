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

command -v curl >/dev/null 2>&1 || fail "需要 curl 才能下载安装包"
command -v tar >/dev/null 2>&1 || fail "需要 tar 才能解压安装包"

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
    fail "不支持的操作系统：$(uname -s)"
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
    fail "不支持的处理器架构：$(uname -m)"
    ;;
esac

asset="${plugin_name}_${target_os}_${target_arch}.tar.gz"
plugin_file="${plugin_name}.${extension}"
download_url="https://github.com/${repository}/releases/latest/download/${asset}"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/${plugin_name}.XXXXXX")" || fail "无法创建临时目录"
archive="${tmp_dir}/${asset}"

printf '正在下载 %s...\n' "$asset"
curl -fL --retry 3 --connect-timeout 15 -o "$archive" "$download_url" \
  || fail "下载失败：${download_url}"

tar -xzf "$archive" -C "$tmp_dir" "$plugin_file" \
  || fail "发布包中缺少 ${plugin_file}"
[ -s "${tmp_dir}/${plugin_file}" ] || fail "发布包中的 ${plugin_file} 为空"

mkdir -p "$plugin_dir" || fail "无法创建 ${plugin_dir}"
staged_file="${plugin_dir}/.${plugin_file}.tmp.$$"
cp "${tmp_dir}/${plugin_file}" "$staged_file" || fail "无法写入 ${plugin_dir}"
chmod 0755 "$staged_file"
mv -f "$staged_file" "${plugin_dir}/${plugin_file}"
staged_file=""

printf '安装完成：%s\n' "${plugin_dir}/${plugin_file}"
printf '请重启 CLIProxyAPI 以加载插件。\n'
