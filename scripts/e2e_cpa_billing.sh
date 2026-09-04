#!/usr/bin/env bash

set -euo pipefail

# Usage: e2e_cpa_billing.sh [目标 ...]
#
# Each argument names one CLIProxyAPI to test, and they are tested in turn:
#
#	不传参数            GitHub 上的最新发布版
#	7.2.136             GitHub 上的该发布版
#	../CLIProxyAPI      代码目录，现场构建
#	../cli-proxy-api    可执行文件，直接使用
readonly github_repo="router-for-me/CLIProxyAPI"
# The upstream is scripts/dummy_provider.py: it answers all four protocols this
# suite routes to, so a run needs no network, no credentials, and returns the
# same token counts every time.
readonly upstream_api_key="dummy-upstream-key-e2e"
readonly expected_input_tokens=128
readonly expected_output_tokens=8
readonly expected_cache_read_tokens=32
readonly expected_cache_write_tokens=16
readonly usage_settle_seconds=1
readonly base_port="${CPA_E2E_BASE_PORT:-28317}"
targets=("$@")
if (( ${#targets[@]} == 0 )); then
  targets=(latest)
fi

for command_name in curl jq tar go python3; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "缺少命令：$command_name" >&2
    exit 1
  fi
done

case "$(uname -s)" in
  Darwin) platform="darwin"; plugin_extension="dylib" ;;
  Linux) platform="linux"; plugin_extension="so" ;;
  *) echo "仅支持 macOS 和 Linux。" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  arm64|aarch64) architecture="aarch64" ;;
  x86_64|amd64) architecture="amd64" ;;
  *) echo "不支持当前处理器架构：$(uname -m)" >&2; exit 1 ;;
esac

script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
repo_dir="$(CDPATH= cd -- "$script_dir/.." && pwd)"
cache_dir="${CPA_E2E_CACHE_DIR:-${TMPDIR:-/tmp}/cpa-key-billing-e2e-cache}"
run_dir="$(mktemp -d "${TMPDIR:-/tmp}/cpa-key-billing-e2e.XXXXXX")"
active_pid=""
upstream_pid=""
upstream_port=""

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ -n "$active_pid" ]]; then
    kill "$active_pid" >/dev/null 2>&1 || true
    wait "$active_pid" >/dev/null 2>&1 || true
  fi
  if [[ -n "$upstream_pid" ]]; then
    kill "$upstream_pid" >/dev/null 2>&1 || true
    wait "$upstream_pid" >/dev/null 2>&1 || true
  fi
  find "$run_dir" -type f -name config.yaml -delete 2>/dev/null || true
  if [[ "${CPA_E2E_KEEP:-0}" == "1" ]]; then
    echo "测试文件保留在：$run_dir"
  else
    rm -rf "$run_dir"
  fi
  exit "$status"
}
trap cleanup EXIT INT TERM

log_stage() {
  printf '\n==> %s\n' "$*"
}

log_step() {
  printf '  - %s\n' "$*"
}

log_ok() {
  printf '  ✓ %s\n' "$*"
}

mkdir -p "$cache_dir" "$run_dir/plugin"
plugin_path="$run_dir/plugin/cpa-key-billing.$plugin_extension"
log_stage "构建计费插件"
(
  cd "$repo_dir"
  GOCACHE="$cache_dir/go-build" CGO_ENABLED=1 \
    go build -buildvcs=false -tags cshared -buildmode=c-shared \
    -o "$plugin_path" ./cmd/cpa-key-billing
)
rm -f "$run_dir/plugin/cpa-key-billing.h"

github_json() {
  curl -fsSL \
    -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    -H "User-Agent: cpa-key-billing-e2e" \
    "$1"
}

latest_version=""
resolve_version() {
  local version="$1"
  if [[ "$version" != "latest" ]]; then
    printf '%s' "${version#v}"
    return
  fi
  if [[ -z "$latest_version" ]]; then
    latest_version="$(github_json "https://api.github.com/repos/$github_repo/releases/latest" | jq -er '.tag_name | ltrimstr("v")')"
  fi
  printf '%s' "$latest_version"
}

checksum_file() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  else
    shasum -a 256 "$file" | awk '{print $1}'
  fi
}

download_host() {
  local version="$1"
  local asset="CLIProxyAPI_${version}_${platform}_${architecture}.tar.gz"
  local archive="$cache_dir/$asset"
  local checksums="$cache_dir/checksums-$version.txt"
  local release_url="https://github.com/$github_repo/releases/download/v$version"
  local expected actual

  if [[ ! -s "$archive" ]]; then
    log_step "下载 CLIProxyAPI v${version}" >&2
    curl -fL --retry 3 --output "$archive.part" "$release_url/$asset"
    mv "$archive.part" "$archive"
  fi
  if [[ ! -s "$checksums" ]]; then
    curl -fsSL --retry 3 --output "$checksums.part" "$release_url/checksums.txt"
    mv "$checksums.part" "$checksums"
  fi
  expected="$(awk -v name="$asset" '$2 == name || $2 == "*" name {print $1; exit}' "$checksums")"
  actual="$(checksum_file "$archive")"
  if [[ -z "$expected" || "$actual" != "$expected" ]]; then
    echo "CLIProxyAPI v$version 校验和不匹配。" >&2
    exit 1
  fi
  printf '%s' "$archive"
}

# resolve_host prepares one target and reports what it is through host_label and
# host_binary. Anything that is not a directory or an executable is read as a
# release version, "latest" included.
host_label=""
host_binary=""
resolve_host() {
  local target="$1"
  local host_dir="$2"
  local version archive

  if [[ -d "$target" ]]; then
    host_label="${target}（源码构建）"
    host_binary="$host_dir/cli-proxy-api"
    log_step "从源码构建 CLIProxyAPI：${target}"
    (
      cd "$target"
      GOCACHE="$cache_dir/go-build" go build -o "$host_binary" ./cmd/server
    )
    return
  fi
  if [[ -f "$target" && -x "$target" ]]; then
    host_label="$target"
    host_binary="$target"
    return
  fi
  version="$(resolve_version "$target")"
  archive="$(download_host "$version")"
  tar -xzf "$archive" -C "$host_dir"
  host_label="v$version"
  host_binary="$(find "$host_dir" -type f -name 'cli-proxy-api' -perm -111 | head -n 1)"
}

# The dummy upstream binds an ephemeral port and reports it on its first line
# of output, so concurrent runs of this suite never contend for one.
start_upstream() {
  local log_file="$run_dir/upstream.log"
  local attempts=0
  python3 "$script_dir/dummy_provider.py" --port 0 >"$log_file" 2>&1 &
  upstream_pid=$!
  while (( attempts < 100 )); do
    upstream_port="$(sed -n 's|^dummy provider: http://127.0.0.1:||p' "$log_file" | head -n 1)"
    if [[ -n "$upstream_port" ]] &&
      curl -fsS --max-time 1 "http://127.0.0.1:$upstream_port/health" >/dev/null 2>&1; then
      return
    fi
    if ! kill -0 "$upstream_pid" >/dev/null 2>&1; then
      echo "dummy provider 启动失败：" >&2
      cat "$log_file" >&2 || true
      return 1
    fi
    attempts=$((attempts + 1))
    sleep 0.1
  done
  echo "等待 dummy provider 启动超时。" >&2
  return 1
}

wait_for_server() {
  local port="$1"
  local attempts=0
  while (( attempts < 120 )); do
    if ! kill -0 "$active_pid" >/dev/null 2>&1; then
      echo "CLIProxyAPI 启动失败。" >&2
      return 1
    fi
    if curl -fsS --max-time 1 "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
      sleep 0.1
      if kill -0 "$active_pid" >/dev/null 2>&1; then
        return
      fi
    fi
    attempts=$((attempts + 1))
    sleep 0.5
  done
  echo "等待 CLIProxyAPI 启动超时。" >&2
  return 1
}

port_available() {
  python3 -c 'import socket, sys
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
try:
    s.bind(("127.0.0.1", int(sys.argv[1])))
except OSError:
    raise SystemExit(1)
finally:
    s.close()' "$1"
}

management_call() {
  local method="$1"
  local port="$2"
  local path="$3"
  shift 3
  curl -fsS --max-time 30 \
    -X "$method" \
    -H "Authorization: Bearer e2e-management-key" \
    "$@" \
    "http://127.0.0.1:$port$path"
}

account_call() {
  local port="$1"
  local path="$2"
  curl -fsS --max-time 30 \
    -H "Authorization: Bearer e2e-downstream-key" \
    "http://127.0.0.1:$port$path"
}

protocol_label() {
  case "$1" in
    chat) printf 'OpenAI Chat' ;;
    responses) printf 'OpenAI Responses' ;;
    anthropic) printf 'Anthropic Messages' ;;
    gemini) printf 'Gemini' ;;
  esac
}

# Each client API authenticates the downstream key its own way.
client_headers() {
  printf '%s\n' "Content-Type: application/json"
  case "$1" in
    anthropic)
      printf '%s\n' "x-api-key: e2e-downstream-key" "anthropic-version: 2023-06-01"
      ;;
    gemini)
      printf '%s\n' "x-goog-api-key: e2e-downstream-key"
      ;;
    *)
      printf '%s\n' "Authorization: Bearer e2e-downstream-key"
      ;;
  esac
}

# The Gemini API names the model and the streaming mode in the path instead of
# the body, so an endpoint is only known once both are.
client_endpoint() {
  local client="$1"
  local requested_model="$2"
  local stream="$3"
  case "$client" in
    chat) printf '/v1/chat/completions' ;;
    responses) printf '/v1/responses' ;;
    anthropic) printf '/v1/messages' ;;
    gemini)
      if [[ "$stream" == "true" ]]; then
        printf '/v1beta/models/%s:streamGenerateContent?alt=sse' "$requested_model"
      else
        printf '/v1beta/models/%s:generateContent' "$requested_model"
      fi
      ;;
  esac
}

api_call() {
  local port="$1"
  local name="$2"
  local path="$3"
  local body="$4"
  local client_format="$5"
  local output="$6"
  local -a headers
  local header_line http_status error_message

  headers=()
  while IFS= read -r header_line; do
    headers+=(-H "$header_line")
  done < <(client_headers "$client_format")
  if ! http_status="$(curl -sS -N --max-time 300 \
    "${headers[@]}" \
    --data "$body" \
    --output "$output" \
    --write-out '%{http_code}' \
    "http://127.0.0.1:$port$path")"; then
    echo "请求失败：$name" >&2
    return 1
  fi
  if [[ "$http_status" != 2* ]]; then
    error_message="$(jq -r '.error.message // .message // empty' "$output" 2>/dev/null || true)"
    echo "请求失败：${name}（HTTP ${http_status}${error_message:+，${error_message}}）" >&2
    return 1
  fi
  if jq -e '(.type? == "error") or (.error? != null)' "$output" >/dev/null 2>&1 ||
    grep -Eq '^data:.*"type"[[:space:]]*:[[:space:]]*"error"' "$output"; then
    echo "上游返回错误：$name" >&2
    return 1
  fi
}

# extract_downstream_usage reports the input and output tokens the client was
# told about, as [total input, total output]. A streaming answer is read down to
# the last usage object its events carried, which is the one that settles the
# turn — and the only one CLIProxyAPI itself bills from.
extract_downstream_usage() {
  local client_format="$1"
  local stream="$2"
  local response_file="$3"
  local usage

  if [[ "$stream" == "true" ]]; then
    usage="$(jq -Rs '
      [ split("\n")[] | sub("\r$"; "") | select(startswith("data:")) | sub("^data:[ ]?"; "") |
        select(. != "[DONE]") | fromjson? |
        (.usage? // .message.usage? // .response.usage? // .usageMetadata?) |
        select(type == "object") ] | last
    ' "$response_file")"
  else
    usage="$(jq '.usage // .usageMetadata' "$response_file")"
  fi

  case "$client_format" in
    chat) jq -er '[.prompt_tokens, .completion_tokens] | @tsv' <<<"$usage" ;;
    responses) jq -er '[.input_tokens, .output_tokens] | @tsv' <<<"$usage" ;;
    # Anthropic keeps the cached buckets beside input_tokens; the others count
    # them inside the prompt total. Reasoning is output wherever it is reported.
    anthropic)
      jq -er '[
        (.input_tokens + (.cache_read_input_tokens // 0) + (.cache_creation_input_tokens // 0)),
        .output_tokens
      ] | @tsv' <<<"$usage"
      ;;
    gemini)
      jq -er '[.promptTokenCount, (.candidatesTokenCount + (.thoughtsTokenCount // 0))] | @tsv' <<<"$usage"
      ;;
  esac
}

# Every request carries an output budget the way a real client does. The dummy
# upstream answers well under it, so nothing is ever truncated.
readonly max_output_tokens=128

request_body() {
  local client="$1"
  local requested_model="$2"
  local stream="$3"
  local prompt="$4"
  local program
  case "$client" in
    chat)
      program='{model: $model, messages: [{role: "user", content: $prompt}], max_tokens: $budget, stream: $stream} +
        (if $stream then {stream_options: {include_usage: true}} else {} end)'
      ;;
    responses)
      program='{
        model: $model,
        input: [{type: "message", role: "user", content: [{type: "input_text", text: $prompt}]}],
        max_output_tokens: $budget,
        stream: $stream
      }'
      ;;
    anthropic)
      program='{model: $model, messages: [{role: "user", content: $prompt}], max_tokens: $budget, stream: $stream}'
      ;;
    gemini)
      # The model and the streaming mode belong to the URL here, so the body
      # carries neither.
      program='{contents: [{role: "user", parts: [{text: $prompt}]}], generationConfig: {maxOutputTokens: $budget}}'
      ;;
  esac
  jq -nc --arg model "$requested_model" --arg prompt "$prompt" \
    --argjson stream "$stream" --argjson budget "$max_output_tokens" "$program"
}

provider_source() {
  local provider
  case "$1" in
    chat) provider="dummy-chat-e2e" ;;
    responses) provider="codex" ;;
    anthropic) provider="claude" ;;
    gemini) provider="gemini" ;;
  esac
  printf '%s · %s…%s' "$provider" "${upstream_api_key:0:6}" "${upstream_api_key: -4}"
}

wait_for_event_count() {
  local port="$1"
  local expected_count="$2"
  local request_events_file="$3"
  local attempt=0 actual_count=0

  while (( attempt < 50 )); do
    if ! management_call GET "$port" "/v0/management/plugins/cpa-key-billing/events?limit=100" >"$request_events_file"; then
      echo "读取请求事件失败。" >&2
      return 1
    fi
    actual_count="$(jq -er '.entries | length' "$request_events_file")"
    if [[ "$actual_count" == "$expected_count" ]]; then
      return
    fi
    if (( actual_count > expected_count )); then
      break
    fi
    attempt=$((attempt + 1))
    sleep 0.1
  done
  echo "请求事件数量为 ${actual_count}，预期 ${expected_count}。" >&2
  return 1
}

assert_billing_entry() {
  local port="$1"
  local expected_count="$2"
  local client="$3"
  local upstream="$4"
  local billing_model="$5"
  local upstream_models="$6"
  local request_events_file="$7"
  local response_file="$8"
  local stream="$9"
  local expected_source expected_executor expected_uncached expected_cache_write usage input output entry_file
  local billed_uncached billed_cache_read billed_cache_write billed_input billed_output
  expected_source="$(provider_source "$upstream")"
  case "$upstream" in
    chat) expected_executor="OpenAICompatExecutor" ;;
    responses) expected_executor="CodexExecutor" ;;
    anthropic) expected_executor="ClaudeExecutor" ;;
    gemini) expected_executor="GeminiExecutor" ;;
  esac
  entry_file="${request_events_file%.json}-entry.json"
  if ! wait_for_event_count "$port" "$expected_count" "$request_events_file"; then
    echo "用例：${client} → ${upstream}。" >&2
    return 1
  fi
  jq -e '.entries[0]' "$request_events_file" >"$entry_file"
  if ! jq -e \
    --arg upstream_models "$upstream_models" \
    --arg billing_model "$billing_model" \
    --arg executor_type "$expected_executor" \
    --arg source "$expected_source" '
      (.upstream_model as $actual | ($upstream_models | split(",") | index($actual)) != null) and
      .billing_model == $billing_model and
      .executor_type == $executor_type and
      .failed == false and
      .source == $source and
      .accounting_quality == "complete" and
      .price_source == "override" and
      (.cost.total_usd > 0)
    ' "$entry_file" >/dev/null; then
    echo "${client} → ${upstream} 的模型、来源、usage 或定价不正确。" >&2
    return 1
  fi

  IFS=$'\t' read -r billed_uncached billed_cache_read billed_cache_write billed_output < <(
    jq -er '.cost | [
      .uncached_input_tokens, .cache_read_tokens, .cache_write_tokens, .billed_output_tokens
    ] | @tsv' "$entry_file"
  )
  expected_cache_write="$expected_cache_write_tokens"
  if [[ "$upstream" == "gemini" ]]; then
    expected_cache_write=0
  fi
  expected_uncached=$((expected_input_tokens - expected_cache_read_tokens - expected_cache_write))
  if [[ "$billed_uncached" != "$expected_uncached" ||
    "$billed_cache_read" != "$expected_cache_read_tokens" ||
    "$billed_cache_write" != "$expected_cache_write" ||
    "$billed_output" != "$expected_output_tokens" ]]; then
    echo "${client} → ${upstream} 的计费 usage 桶不符合上游响应。" >&2
    return 1
  fi
  # Cross-protocol translators may merge cache buckets in the client-facing
  # schema. Only a same-protocol response can be compared bucket-for-bucket via
  # its aggregate totals; the upstream-specific assertion above is authoritative
  # for every conversion.
  if [[ "$client" != "$upstream" ]]; then
    return
  fi
  usage="$(extract_downstream_usage "$client" "$stream" "$response_file")"
  IFS=$'\t' read -r input output <<<"$usage"
  billed_input=$((billed_uncached + billed_cache_read + billed_cache_write))
  if [[ "$input" != "$billed_input" || "$output" != "$billed_output" ]]; then
    echo "${client} 直通响应与计费 usage 不一致。" >&2
    return 1
  fi
}

# assert_route_model_policy binds a route that does not allow the model this
# suite calls, and asserts the request is refused before it can reach
# an upstream: 403 in the client's own error shape, no request event, and one
# line in the plugin log, which is the only record such a request leaves.
assert_route_model_policy() {
  local port="$1"
  local runtime_dir="$2"
  local expected_count="$3"
  local scope route body http_status response_file request_events_file plugin_logs_file

  response_file="$runtime_dir/responses/model-blocked.json"
  request_events_file="$runtime_dir/route-policy-request-events.json"
  plugin_logs_file="$runtime_dir/route-policy-plugin-logs.json"

  # Synchronize the Key list the way the panel does, so this holds whether or
  # not traffic has already created the record.
  management_call POST "$port" "/v0/management/plugins/cpa-key-billing/keys/sync" \
    -H "Content-Type: application/json" \
    --data '{"keys":["e2e-downstream-key"]}' \
    >/dev/null
  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/access" >"$runtime_dir/access.json"
  scope="$(jq -er 'first(.keys[] | select(.in_config) | .scope)' "$runtime_dir/access.json")"
  if ! jq -e --arg scope "$scope" 'first(.keys[] | select(.scope == $scope)) | all(.route_bindings[]; length == 0)' \
    "$runtime_dir/access.json" >/dev/null; then
    echo "未绑定路由的 API Key 仍然受到限制。" >&2
    return 1
  fi

  management_call POST "$port" "/v0/management/plugins/cpa-key-billing/routes" \
    -H "Content-Type: application/json" \
    --data '{"name":"e2e-限定路由","rule":{"models":["codex/gpt-5.6-sol"],"credential_ids":[],"credential_providers":[]}}' \
	>"$runtime_dir/route.json"
  route="$(jq -er '.route.id' "$runtime_dir/route.json")"
  management_call PUT "$port" "/v0/management/plugins/cpa-key-billing/keys/routes" \
    -H "Content-Type: application/json" \
    --data "$(jq -nc --arg scope "$scope" --arg route "$route" '{scope: $scope, bindings: {route_ids:[$route],models:[],credential_ids:[],credential_providers:[{source:"ai-providers",provider:"openai-compatible-dummy-chat-e2e"}]}}')" \
    >/dev/null

  # A thinking suffix is a request option rather than a model of its own, so it
  # is no way around the refusal, and the refusal names the base model.
  for requested in "gpt-5.6-sol" "gpt-5.6-sol(high)" "gpt-5.6-sol(max)"; do
    http_status="$(curl -sS --max-time 30 \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer e2e-downstream-key" \
      --data "$(request_body chat "$requested" false "Reply with exactly OK.")" \
      --output "$response_file" \
      --write-out '%{http_code}' \
      "http://127.0.0.1:$port/v1/chat/completions")"
    if [[ "$http_status" != "403" ]]; then
      echo "无权使用的模型 ${requested} 返回 HTTP ${http_status}，预期 403。" >&2
      return 1
    fi
    if ! jq -e --arg model "gpt-5.6-sol" '
        .error.type == "permission_error" and .error.code == "insufficient_quota"
        and (.error.message | contains("\"" + $model + "\""))' "$response_file" >/dev/null; then
      echo "模型拦截 ${requested} 的错误内容不正确：$(jq -c '.' "$response_file")" >&2
      return 1
    fi
  done

  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/events?limit=100" >"$request_events_file"
  if [[ "$(jq -er '.entries | length' "$request_events_file")" != "$expected_count" ]]; then
    echo "被拦截的请求进入了请求事件。" >&2
    return 1
  fi
  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/plugin-logs?level=debug" >"$plugin_logs_file"
  if ! jq -e --arg model "gpt-5.6-sol" '
      [.entries[]
        | select(.level == "debug" and (.message | startswith("route ")))
        | (.message | gsub("&#34;"; "\"") | ltrimstr("route ") | fromjson)
        | select(.model == $model and .model_result == "deny" and .credential_result == "not_reached")]
      | length == 3' "$plugin_logs_file" >/dev/null; then
    echo "插件日志缺少合并后的模型路由记录：$(jq -c '.entries' "$plugin_logs_file")" >&2
    return 1
  fi

  # Clear the routing restrictions. The request that follows has to be billed
  # exactly like any other.
  management_call PUT "$port" "/v0/management/plugins/cpa-key-billing/keys/routes" \
    -H "Content-Type: application/json" \
    --data "$(jq -nc --arg scope "$scope" '{scope: $scope, bindings: {}}')" \
    >/dev/null
  management_call DELETE "$port" "/v0/management/plugins/cpa-key-billing/routes?id=$route" >/dev/null

  body="$(request_body chat "gpt-5.6-sol" false "Reply with exactly OK.")"
  api_call "$port" "模型拦截解除后：OpenAI Chat → OpenAI Chat 非流式" \
    "/v1/chat/completions" "$body" chat "$runtime_dir/responses/model-restored.json"
  assert_billing_entry "$port" "$((expected_count + 1))" chat chat \
    "gpt-5.6-sol" "gpt-5.6-sol" "$runtime_dir/model-restored-request-events.json" \
    "$runtime_dir/responses/model-restored.json" false
}

# Verify that a source-qualified Provider rule and an exact-Credential rule both
# narrow CPA's candidate set instead of merely changing what the UI displays.
assert_route_credential_policy() {
  local port="$1"
  local runtime_dir="$2"
  local expected_count="$3"
  local scope route allowed_ref body http_status response_file
  local access_file events_file plugin_logs_file

  access_file="$runtime_dir/credential-route-access.json"
  events_file="$runtime_dir/credential-route-events.json"
  plugin_logs_file="$runtime_dir/credential-route-plugin-logs.json"

  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/access" >"$access_file"
  scope="$(jq -er 'first(.keys[] | select(.in_config) | .scope)' "$access_file")"
  management_call POST "$port" "/v0/management/plugins/cpa-key-billing/routes" \
    -H "Content-Type: application/json" \
    --data "$(jq -nc --arg scope "$scope" '{name:"e2e-凭证路由",rule:{models:["e2e-credential-route"],credential_ids:[],credential_providers:[{source:"ai-providers",provider:"openai-compatible-route-allowed-e2e"}]},scopes:[$scope]}')" \
    >"$runtime_dir/credential-route.json"
  route="$(jq -er '.route.id' "$runtime_dir/credential-route.json")"

  body="$(request_body chat "e2e-credential-route" false "Reply with exactly OK.")"
  api_call "$port" "凭证类别路由：仅允许 route-allowed-e2e" \
    "/v1/chat/completions" "$body" chat "$runtime_dir/responses/credential-provider-route.json"
  wait_for_event_count "$port" "$((expected_count + 1))" "$events_file"
  if ! jq -e '
      .entries[0] |
      .provider == "openai-compatible-route-allowed-e2e" and
      .billing_model == "e2e-credential-route" and
      .executor_type == "OpenAICompatExecutor" and .failed == false
    ' "$events_file" >/dev/null; then
    echo "凭证类别路由选择了错误的 Provider：$(jq -c '.entries[0]' "$events_file")" >&2
    return 1
  fi

  # scheduler.pick has now observed both config-backed candidates, so the safe
  # inventory contains the opaque reference needed to test an exact binding.
  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/access" >"$access_file"
  if ! jq -e '
      first(.credentials[] | select(.source == "ai-providers" and .provider == "openai-compatible-route-allowed-e2e")) |
      .display_name == "e2e-ro…1111"
    ' "$access_file" >/dev/null; then
    echo "配置型上游凭证未显示安全的 API Key 掩码：$(jq -c '.credentials' "$access_file")" >&2
    return 1
  fi
  allowed_ref="$(jq -er 'first(.credentials[] | select(.source == "ai-providers" and .provider == "openai-compatible-route-allowed-e2e")).ref' "$access_file")"
  management_call PATCH "$port" "/v0/management/plugins/cpa-key-billing/routes" \
    -H "Content-Type: application/json" \
    --data "$(jq -nc --arg id "$route" --arg ref "$allowed_ref" '{id:$id,rule:{models:["e2e-credential-route"],credential_ids:[$ref],credential_providers:[]}}')" \
    >/dev/null

  api_call "$port" "指定凭证路由：仅允许 route-allowed-e2e" \
    "/v1/chat/completions" "$body" chat "$runtime_dir/responses/credential-exact-route.json"
  wait_for_event_count "$port" "$((expected_count + 2))" "$events_file"
  if ! jq -e '
      [.entries[] | select(.billing_model == "e2e-credential-route")] as $rows |
      ($rows | length) == 2 and all($rows[]; .provider == "openai-compatible-route-allowed-e2e")
    ' "$events_file" >/dev/null; then
    echo "指定凭证路由未稳定限制候选集：$(jq -c '.entries' "$events_file")" >&2
    return 1
  fi

  management_call PATCH "$port" "/v0/management/plugins/cpa-key-billing/routes" \
    -H "Content-Type: application/json" \
    --data "$(jq -nc --arg id "$route" '{id:$id,rule:{models:["e2e-credential-route"],credential_ids:[],credential_providers:[{source:"ai-providers",provider:"openai-compatible-route-missing-e2e"}]}}')" \
    >/dev/null
  response_file="$runtime_dir/responses/credential-route-blocked.json"
  http_status="$(curl -sS --max-time 30 \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer e2e-downstream-key" \
    --data "$body" \
    --output "$response_file" \
    --write-out '%{http_code}' \
    "http://127.0.0.1:$port/v1/chat/completions")"
  if [[ "$http_status" != "503" ]] || ! jq -e '
      .error.type == "server_error" and .error.code == "internal_server_error" and
      (.error.message | contains("当前没有符合路由规则且可用的上游凭证"))
    ' "$response_file" >/dev/null; then
    echo "没有合格凭证时未按预期返回 503：HTTP $http_status $(jq -c '.' "$response_file")" >&2
    return 1
  fi
  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/events?limit=100" >"$events_file"
  if [[ "$(jq -er '.entries | length' "$events_file")" != "$((expected_count + 2))" ]]; then
    echo "凭证路由拦截进入了请求事件。" >&2
    return 1
  fi

  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/plugin-logs?level=debug" >"$plugin_logs_file"
  if ! jq -e '
      [.entries[]
        | select(.level == "debug" and (.message | startswith("route ")))
        | (.message | gsub("&#34;"; "\"") | ltrimstr("route ") | fromjson)
        | select(.model == "e2e-credential-route")] as $rows |
      ($rows | length) == 3 and
      ([ $rows[] | select(.status == 200) ] | length) == 2 and
      ([ $rows[] | select(.status == 503 and .credential_result == "no_match") ] | length) == 1 and
      all($rows[] | select(.status == 200);
        .model_result == "allow" and .credential_result == "selected" and
        ((.selected_credential // "") | length) > 0 and
        ((.selected_credential // "") | contains("上游凭证") | not))
    ' "$plugin_logs_file" >/dev/null; then
    echo "插件日志缺少凭证路由选择结果：$(jq -c '.entries' "$plugin_logs_file")" >&2
    return 1
  fi

  management_call PUT "$port" "/v0/management/plugins/cpa-key-billing/keys/routes" \
    -H "Content-Type: application/json" \
    --data "$(jq -nc --arg scope "$scope" '{scope:$scope,bindings:{}}')" \
    >/dev/null
  management_call DELETE "$port" "/v0/management/plugins/cpa-key-billing/routes?id=$route" >/dev/null
}

# Hold one SSE request open, verify a second request is refused, then verify the
# slot is released when the first request completes.
assert_concurrency_limit() {
  local port="$1"
  local runtime_dir="$2"
  local expected_count="$3"
  local scope body endpoint hold_pid attempt current http_status retry_after actual_count
  local access_file response_file blocked_file headers_file request_events_file request_log

  access_file="$runtime_dir/concurrency-access.json"
  response_file="$runtime_dir/responses/concurrency-held.sse"
  blocked_file="$runtime_dir/responses/concurrency-blocked.json"
  headers_file="$runtime_dir/concurrency-blocked-headers.txt"
  request_events_file="$runtime_dir/concurrency-request-events.json"
  request_log="$runtime_dir/responses/concurrency-held.log"

  management_call POST "$port" "/v0/management/plugins/cpa-key-billing/keys/sync" \
    -H "Content-Type: application/json" \
    --data '{"keys":["e2e-downstream-key"]}' \
    >/dev/null
  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/access" >"$access_file"
  scope="$(jq -er 'first(.keys[] | select(.in_config) | .scope)' "$access_file")"
  management_call POST "$port" "/v0/management/plugins/cpa-key-billing/keys/concurrency" \
    -H "Content-Type: application/json" \
    --data "$(jq -nc --arg scope "$scope" '{scope: $scope, concurrency_limit: 1}')" \
    >/dev/null

  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/access" >"$access_file"
  if ! jq -e --arg scope "$scope" '
      first(.keys[] | select(.scope == $scope)) |
      .concurrency_limit == 1 and .current_concurrency == 0
    ' "$access_file" >/dev/null; then
    echo "API Key 并发数保存结果不正确。" >&2
    return 1
  fi

  body="$(request_body responses "gpt-5.6-sol" true "E2E HOLD CONCURRENCY SLOT")"
  endpoint="$(client_endpoint responses "gpt-5.6-sol" true)"
  api_call "$port" "并发限制：保持 SSE 请求" "$endpoint" "$body" responses "$response_file" \
    >"$request_log" 2>&1 &
  hold_pid=$!

  current=0
  for ((attempt = 0; attempt < 100; attempt++)); do
    management_call GET "$port" "/v0/management/plugins/cpa-key-billing/access" >"$access_file"
    current="$(jq -er --arg scope "$scope" 'first(.keys[] | select(.scope == $scope)).current_concurrency' "$access_file")"
    if [[ "$current" == "1" ]]; then
      break
    fi
    sleep 0.05
  done
  if [[ "$current" != "1" ]]; then
    echo "SSE 请求未占用 API Key 并发槽位。" >&2
    wait "$hold_pid" || cat "$request_log" >&2
    return 1
  fi

  body="$(request_body chat "gpt-5.6-sol" false "Reply with exactly OK.")"
  http_status="$(curl -sS --max-time 30 \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer e2e-downstream-key" \
    --data "$body" \
    --dump-header "$headers_file" \
    --output "$blocked_file" \
    --write-out '%{http_code}' \
    "http://127.0.0.1:$port/v1/chat/completions")"
  if [[ "$http_status" != "429" ]] || ! jq -e '
      .error.type == "rate_limit_error" and
      .error.code == "rate_limit_exceeded" and
      (.error.message | startswith("API key concurrency limit reached"))
    ' "$blocked_file" >/dev/null; then
    echo "并发饱和请求的响应不正确（HTTP ${http_status}）：$(jq -c '.' "$blocked_file")" >&2
    wait "$hold_pid" || true
    return 1
  fi
  retry_after="$(awk 'tolower($1) == "retry-after:" {gsub(/\r/, "", $2); print $2}' "$headers_file" | tail -n 1)"
  if [[ "$retry_after" != "1" ]]; then
    echo "并发饱和请求缺少 Retry-After: 1。" >&2
    wait "$hold_pid" || true
    return 1
  fi

  if ! wait "$hold_pid"; then
    cat "$request_log" >&2
    return 1
  fi
  current=1
  for ((attempt = 0; attempt < 100; attempt++)); do
    management_call GET "$port" "/v0/management/plugins/cpa-key-billing/access" >"$access_file"
    current="$(jq -er --arg scope "$scope" 'first(.keys[] | select(.scope == $scope)).current_concurrency' "$access_file")"
    if [[ "$current" == "0" ]]; then
      break
    fi
    sleep 0.05
  done
  if [[ "$current" != "0" ]]; then
    echo "SSE 请求完成后 API Key 并发槽位未释放。" >&2
    return 1
  fi

  if ! wait_for_event_count "$port" "$((expected_count + 1))" "$request_events_file"; then
    return 1
  fi
  actual_count="$(jq -er '.entries | length' "$request_events_file")"
  if [[ "$actual_count" != "$((expected_count + 1))" ]]; then
    echo "并发拦截请求进入了请求事件。" >&2
    return 1
  fi

  management_call POST "$port" "/v0/management/plugins/cpa-key-billing/keys/concurrency" \
    -H "Content-Type: application/json" \
    --data "$(jq -nc --arg scope "$scope" '{scope: $scope, concurrency_limit: 0}')" \
    >/dev/null
}

# assert_quota_exhausted binds the downstream key to a plan one request is enough
# to spend, and asserts every client format is then refused before it can reach
# an upstream: 429 in the client's own error shape with a Retry-After hint,
# no request event, and one line in the plugin log — an exhausted key
# names itself once per cycle however often the client behind it retries.
assert_quota_exhausted() {
  local port="$1"
  local runtime_dir="$2"
  local expected_count="$3"
  local scope plan client endpoint body header_line http_status retry_after actual_count
  local response_file headers_file request_events_file plugin_logs_file
  local plan_name="e2e-额度计划"
  local -a headers

  response_file="$runtime_dir/responses/quota-blocked.json"
  headers_file="$runtime_dir/quota-blocked-headers.txt"
  request_events_file="$runtime_dir/quota-request-events.json"
  plugin_logs_file="$runtime_dir/quota-plugin-logs.json"

  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/access" >"$runtime_dir/access.json"
  scope="$(jq -er 'first(.keys[] | select(.in_config) | .scope)' "$runtime_dir/access.json")"

  # A budget below what one request costs. Nothing has been spent when the
  # request below is admitted, which is what makes it the one that exhausts the
  # plan rather than the one that is refused.
  management_call POST "$port" "/v0/management/plugins/cpa-key-billing/plans" \
    -H "Content-Type: application/json" \
    --data "$(jq -nc --arg name "$plan_name" --arg scope "$scope" \
      '{name: $name, amount_usd: 0.0001, period_seconds: 86400, scopes: [$scope]}')" \
    >"$runtime_dir/plan.json"
  plan="$(jq -er '.plan.id' "$runtime_dir/plan.json")"

  # The dummy provider's fixed usage costs more than this plan allows.
  body="$(request_body chat "gpt-5.6-sol" false "Reply with exactly OK.")"
  api_call "$port" "额度耗尽前：OpenAI Chat → OpenAI Chat 非流式" \
    "/v1/chat/completions" "$body" chat "$runtime_dir/responses/quota-spend.json"
  expected_count=$((expected_count + 1))
  assert_billing_entry "$port" "$expected_count" chat chat \
    "gpt-5.6-sol" "gpt-5.6-sol" "$runtime_dir/quota-spend-request-events.json" \
    "$runtime_dir/responses/quota-spend.json" false

  # The model stays one the key may call, so only the exhausted budget can be
  # refusing these. Anthropic clients read an error envelope of their own; every
  # other client, Gemini included, reads the OpenAI-shaped one.
  for client in chat responses anthropic gemini; do
    headers=()
    while IFS= read -r header_line; do
      headers+=(-H "$header_line")
    done < <(client_headers "$client")
    body="$(request_body "$client" "gpt-5.6-sol" false "Reply with exactly OK.")"
    endpoint="$(client_endpoint "$client" "gpt-5.6-sol" false)"
    http_status="$(curl -sS --max-time 30 \
      "${headers[@]}" \
      --data "$body" \
      --dump-header "$headers_file" \
      --output "$response_file" \
      --write-out '%{http_code}' \
      "http://127.0.0.1:$port$endpoint")"
    if [[ "$http_status" != "429" ]]; then
      echo "额度耗尽后 ${client} 返回 HTTP ${http_status}，预期 429。" >&2
      return 1
    fi
    if ! jq -e --arg client "$client" --arg plan "$plan_name" '
        (if $client == "anthropic"
         then .type == "error" and .error.type == "rate_limit_error"
         else .error.type == "rate_limit_error" and .error.code == "rate_limit_exceeded"
         end)
        and (.error.message | startswith("API key subscription quota exhausted"))
        and (.error.message | contains("on plan \"" + $plan + "\""))
      ' "$response_file" >/dev/null; then
      echo "额度拦截 ${client} 的错误内容不正确：$(jq -c '.' "$response_file")" >&2
      return 1
    fi
    # A periodic plan resets, so the refusal tells the client how long to wait.
    retry_after="$(awk 'tolower($1) == "retry-after:" {gsub(/\r/, "", $2); print $2}' "$headers_file" | tail -n 1)"
    if [[ -z "$retry_after" ]] || (( retry_after <= 0 )); then
      echo "额度拦截 ${client} 缺少 Retry-After 响应头：${retry_after:-无}" >&2
      return 1
    fi
  done

  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/events?limit=100" >"$request_events_file"
  actual_count="$(jq -er '.entries | length' "$request_events_file")"
  if [[ "$actual_count" != "$expected_count" ]]; then
    echo "被额度拦截的请求进入了请求事件：${actual_count}，预期 ${expected_count}。" >&2
    return 1
  fi
  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/plugin-logs" >"$plugin_logs_file"
  if ! jq -e '[.entries[] | select(.level == "info" and (.message | startswith("额度拦截：")))] | length == 1' \
    "$plugin_logs_file" >/dev/null; then
    echo "插件日志的额度拦截记录数量不正确：$(jq -c '[.entries[] | select(.message | startswith("额度拦截："))]' "$plugin_logs_file")" >&2
    return 1
  fi

  # Unbinding the plan makes the key unlimited again, which is what the panel
  # does to release one, and the request that follows has to be billed as usual.
  management_call POST "$port" "/v0/management/plugins/cpa-key-billing/keys/unbind" \
    -H "Content-Type: application/json" \
    --data "$(jq -nc --arg scope "$scope" '{scope: $scope}')" \
    >/dev/null
  management_call DELETE "$port" "/v0/management/plugins/cpa-key-billing/plans?id=$plan" >/dev/null

  body="$(request_body chat "gpt-5.6-sol" false "Reply with exactly OK.")"
  api_call "$port" "额度拦截解除后：OpenAI Chat → OpenAI Chat 非流式" \
    "/v1/chat/completions" "$body" chat "$runtime_dir/responses/quota-restored.json"
  assert_billing_entry "$port" "$((expected_count + 1))" chat chat \
    "gpt-5.6-sol" "gpt-5.6-sol" "$runtime_dir/quota-restored-request-events.json" \
    "$runtime_dir/responses/quota-restored.json" false
}

run_target() {
  local target="$1"
  local index="$2"
  local port=$((base_port + index))
  local target_dir="$run_dir/target-$index"
  local host_dir="$target_dir/host"
  local runtime_dir="$target_dir/runtime"
  local api_key_json plugins_file prompt account_status_file account_access_file account_prices_file account_events_file
  local client upstream stream endpoint body mode extension response_file request_events_file
  local client_label upstream_label mode_label request_number requested_model billing_model upstream_models model_id
  local model_case case_name actual_upstream_model expected_source expected_uncached expected_cache_write
  local request_index expected_requests actual_requests matrix_index matrix_failed matrix_logs_ok matches usage input output
  local -a provider_cases model_cases matrix_pids matrix_labels matrix_clients matrix_upstreams matrix_streams
  local -a matrix_models matrix_upstream_models matrix_responses matrix_errors matrix_request_ok matrix_ok matrix_reasons
  mkdir -p "$host_dir" "$runtime_dir/plugins" "$runtime_dir/auth" "$runtime_dir/responses"

  log_stage "目标 $((index + 1))/${#targets[@]}：${target}"
  resolve_host "$target" "$host_dir"
  if [[ -z "$host_binary" || ! -x "$host_binary" ]]; then
    echo "CLIProxyAPI ${host_label} 缺少可执行文件：$host_binary" >&2
    return 1
  fi
  cp "$plugin_path" "$runtime_dir/plugins/cpa-key-billing.$plugin_extension"
  cp "$repo_dir/internal/billing/testdata/catalog.json" "$runtime_dir/catalog.json"

  # The models and providers live in scripts/e2e_config.yaml; only runtime paths,
  # ports and the dummy credential are filled in here.
  api_key_json="$(printf '%s' "$upstream_api_key" | jq -Rs .)"
  sed \
    -e "s|__PORT__|$port|g" \
    -e "s|__RUNTIME_DIR__|$runtime_dir|g" \
    -e "s|__UPSTREAM_ORIGIN__|http://127.0.0.1:$upstream_port|g" \
    -e "s|__UPSTREAM_API_KEY__|$api_key_json|g" \
    "$script_dir/e2e_config.yaml" >"$runtime_dir/config.yaml"
  if grep -q '__[A-Z_]*__' "$runtime_dir/config.yaml"; then
    echo "配置模板存在未替换的占位符：$(grep -o '__[A-Z_]*__' "$runtime_dir/config.yaml" | sort -u | tr '\n' ' ')" >&2
    return 1
  fi
  chmod 600 "$runtime_dir/config.yaml"

  if ! port_available "$port"; then
    echo "测试端口已被占用：127.0.0.1:${port}。" >&2
    return 1
  fi
  log_step "启动 ${host_label}，监听 127.0.0.1:${port}"
  CPA_KEY_BILLING_CATALOG_CACHE="$runtime_dir/catalog.json" \
    "$host_binary" -config "$runtime_dir/config.yaml" -local-model >"$runtime_dir/host.log" 2>&1 &
  active_pid=$!
  if ! wait_for_server "$port"; then
    tail -n 80 "$runtime_dir/host.log" >&2 || true
    return 1
  fi

  plugins_file="$runtime_dir/plugins.json"
  management_call GET "$port" "/v0/management/plugins" >"$plugins_file"
  if ! jq -e '.plugins[] | select(.id == "cpa-key-billing" and .registered == true and .effective_enabled == true)' "$plugins_file" >/dev/null; then
    echo "插件未在 CLIProxyAPI ${host_label} 中注册。" >&2
    tail -n 80 "$runtime_dir/host.log" >&2 || true
    return 1
  fi
  log_step "插件已注册并启用"

  # One wildcard makes every test model deterministic without registering five
  # identical overrides or consulting the external reference catalog.
  management_call PUT "$port" "/v0/management/plugins/cpa-key-billing/prices" \
    -H "Content-Type: application/json" \
    --data '{"pattern":"*","input_per_1m":1,"output_per_1m":2,"cache_read_per_1m":0.1,"cache_write_per_1m":1.25}' \
    >"$runtime_dir/price.json"
  log_step "测试价格已配置"

  prompt="Reply with exactly OK."
  request_index=0
  # Exercise every client/upstream conversion in both modes. Billing must match
  # the dummy upstream's fixed usage buckets; direct responses are checked too.
  provider_cases=(chat responses anthropic gemini)
  matrix_pids=()
  matrix_labels=()
  matrix_clients=()
  matrix_upstreams=()
  matrix_streams=()
  matrix_models=()
  matrix_upstream_models=()
  matrix_responses=()
  matrix_errors=()
  log_step "并发执行 32 个协议转换与 usage 用例"
  for client in chat responses anthropic gemini; do
    client_label="$(protocol_label "$client")"
    for upstream in "${provider_cases[@]}"; do
      upstream_label="$(protocol_label "$upstream")"
      for stream in false true; do
        request_index=$((request_index + 1))
        if [[ "$stream" == "true" ]]; then
          mode="stream"; mode_label="流式"; extension="sse"
        else
          mode="nonstream"; mode_label="非流式"; extension="json"
        fi
        model_id="e2e-${client}-to-${upstream}-${mode}"
        requested_model="$model_id"
        if [[ "$upstream" == "responses" ]]; then
          requested_model="codex/$model_id"
        fi
        billing_model="$requested_model"
        upstream_models="$model_id"
        body="$(request_body "$client" "$requested_model" "$stream" "$prompt")"
        endpoint="$(client_endpoint "$client" "$requested_model" "$stream")"
        printf -v request_number '%02d' "$request_index"
        response_file="$runtime_dir/responses/${request_number}-${client}-to-${upstream}-${mode}.${extension}"
        matrix_labels+=("${client_label} → ${upstream_label} ${mode_label}")
        matrix_clients+=("$client")
        matrix_upstreams+=("$upstream")
        matrix_streams+=("$stream")
        matrix_models+=("$billing_model")
        matrix_upstream_models+=("$upstream_models")
        matrix_responses+=("$response_file")
        matrix_errors+=("$runtime_dir/responses/${request_number}-request.log")
        api_call "$port" "${client_label} → ${upstream_label} ${mode_label}" \
          "$endpoint" "$body" "$client" "$response_file" \
          >"${matrix_errors[$((request_index - 1))]}" 2>&1 &
        matrix_pids+=("$!")
      done
    done
  done

  for ((matrix_index = 0; matrix_index < 32; matrix_index++)); do
    if wait "${matrix_pids[$matrix_index]}"; then
      matrix_request_ok[$matrix_index]=1
    else
      matrix_request_ok[$matrix_index]=0
    fi
  done
  sleep "$usage_settle_seconds"

  request_events_file="$runtime_dir/matrix-billing.json"
  matrix_logs_ok=1
  if ! management_call GET "$port" "/v0/management/plugins/cpa-key-billing/events?limit=100" >"$request_events_file"; then
    matrix_logs_ok=0
  fi

  matrix_failed=0
  for ((matrix_index = 0; matrix_index < 32; matrix_index++)); do
    matrix_ok[$matrix_index]=0
    if [[ "${matrix_request_ok[$matrix_index]}" != "1" ]]; then
      matrix_reasons[$matrix_index]="请求失败"
      matrix_failed=$((matrix_failed + 1))
      continue
    fi
    if (( matrix_logs_ok == 0 )); then
      matrix_reasons[$matrix_index]="日志读取失败"
      matrix_failed=$((matrix_failed + 1))
      continue
    fi

    billing_model="${matrix_models[$matrix_index]}"
    upstream="${matrix_upstreams[$matrix_index]}"
    matches="$(jq -er --arg model "$billing_model" '[.entries[] | select(.billing_model == $model)] | length' "$request_events_file")"
    if [[ "$matches" == "0" ]]; then
      matrix_reasons[$matrix_index]="漏记"
      matrix_failed=$((matrix_failed + 1))
      continue
    fi
    if [[ "$matches" != "1" ]]; then
      matrix_reasons[$matrix_index]="重复 ${matches} 条"
      matrix_failed=$((matrix_failed + 1))
      continue
    fi

    expected_source="$(provider_source "$upstream")"
    expected_cache_write="$expected_cache_write_tokens"
    if [[ "$upstream" == "gemini" ]]; then
      expected_cache_write=0
    fi
    expected_uncached=$((expected_input_tokens - expected_cache_read_tokens - expected_cache_write))
    if ! jq -e \
      --arg billing_model "$billing_model" \
      --arg upstream_model "${matrix_upstream_models[$matrix_index]}" \
      --arg source "$expected_source" '
        first(.entries[] | select(.billing_model == $billing_model)) |
        .upstream_model == $upstream_model and
        .source == $source and
        .failed == false and
        .accounting_quality == "complete" and
        .price_source == "override" and
        (.latency_ms // 0) > 0 and
        (.ttft_ms // 0) > 0 and
        .ttft_ms <= .latency_ms
      ' "$request_events_file" >/dev/null; then
      matrix_reasons[$matrix_index]="模型、来源或延迟不符"
      matrix_failed=$((matrix_failed + 1))
      continue
    fi
    if ! jq -e \
      --arg billing_model "$billing_model" \
      --argjson uncached "$expected_uncached" \
      --argjson cache_read "$expected_cache_read_tokens" \
      --argjson cache_write "$expected_cache_write" \
      --argjson output "$expected_output_tokens" '
        first(.entries[] | select(.billing_model == $billing_model)).cost |
        .uncached_input_tokens == $uncached and
        .cache_read_tokens == $cache_read and
        .cache_write_tokens == $cache_write and
        .billed_output_tokens == $output and
        .total_usd > 0
      ' "$request_events_file" >/dev/null; then
      matrix_reasons[$matrix_index]="usage 不符"
      matrix_failed=$((matrix_failed + 1))
      continue
    fi

    if [[ "${matrix_clients[$matrix_index]}" == "$upstream" ]]; then
      if ! usage="$(extract_downstream_usage \
        "${matrix_clients[$matrix_index]}" "${matrix_streams[$matrix_index]}" \
        "${matrix_responses[$matrix_index]}" 2>/dev/null)"; then
        matrix_reasons[$matrix_index]="响应 usage 缺失"
        matrix_failed=$((matrix_failed + 1))
        continue
      fi
      IFS=$'\t' read -r input output <<<"$usage"
      if [[ "$input" != "$expected_input_tokens" || "$output" != "$expected_output_tokens" ]]; then
        matrix_reasons[$matrix_index]="响应 usage 不符"
        matrix_failed=$((matrix_failed + 1))
        continue
      fi
    fi
    matrix_ok[$matrix_index]=1
    matrix_reasons[$matrix_index]=""
  done

  for ((matrix_index = 0; matrix_index < 32; matrix_index++)); do
    if [[ "${matrix_ok[$matrix_index]}" == "1" ]]; then
      printf '    [%02d/32] ✓ %s\n' "$((matrix_index + 1))" "${matrix_labels[$matrix_index]}"
    else
      printf '    [%02d/32] ✗ %s（%s）\n' \
        "$((matrix_index + 1))" "${matrix_labels[$matrix_index]}" "${matrix_reasons[$matrix_index]}"
    fi
  done
  if (( matrix_failed != 0 )); then
    printf '  ✗ 协议转换：%d/32 通过，%d/32 失败\n' \
      "$((32 - matrix_failed))" "$matrix_failed"
    return 1
  fi
  log_ok "协议转换：32/32 通过"

  # 名称|客户端|上游|流式|请求模型|计费模型|可能的上游模型
  model_cases=(
    "推理后缀|chat|chat|false|gpt-5.6-sol(high)|gpt-5.6-sol|gpt-5.6-sol"
    "前缀与推理后缀|anthropic|responses|true|codex/gpt-5.6-sol(high)|codex/gpt-5.6-sol|gpt-5.6-sol"
    "模型池|gemini|chat|false|gpt-auto|gpt-auto|gpt-5.6-sol,gpt-5.5"
  )
  log_step "模型路由：推理后缀、前缀与模型池"
  for model_case in "${model_cases[@]}"; do
    IFS='|' read -r case_name client upstream stream requested_model billing_model upstream_models <<<"$model_case"
    client_label="$(protocol_label "$client")"
    upstream_label="$(protocol_label "$upstream")"
    request_index=$((request_index + 1))
    if [[ "$stream" == "true" ]]; then
      mode="stream"; mode_label="流式"; extension="sse"
    else
      mode="nonstream"; mode_label="非流式"; extension="json"
    fi
    body="$(request_body "$client" "$requested_model" "$stream" "$prompt")"
    endpoint="$(client_endpoint "$client" "$requested_model" "$stream")"
    printf -v request_number '%02d' "$request_index"
    response_file="$runtime_dir/responses/${request_number}-${case_name}-${mode}.${extension}"
    request_events_file="$runtime_dir/responses/${request_number}-billing.json"
    api_call "$port" "${case_name}：${client_label} → ${upstream_label} ${mode_label}" \
      "$endpoint" "$body" "$client" "$response_file"
    assert_billing_entry "$port" "$request_index" "$client" "$upstream" \
      "$billing_model" "$upstream_models" "$request_events_file" "$response_file" "$stream"
    actual_upstream_model="$(jq -er '.entries[0].upstream_model' "$request_events_file")"
    printf '    [%d/3] %s：请求 %s；计费 %s；上游 %s\n' \
      "$((request_index - 32))" "$case_name" "$requested_model" "$billing_model" "$actual_upstream_model"
  done

  request_events_file="$runtime_dir/request-events.json"
  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/events?limit=100" >"$request_events_file"
  # Four client protocols against four upstream protocols in both modes, plus
  # the three routing cases.
  expected_requests=35
  actual_requests="$(jq -er '.entries | length' "$request_events_file")"
  if [[ "$actual_requests" != "$expected_requests" ]]; then
    echo "CLIProxyAPI ${host_label} 请求事件数量为 ${actual_requests}，预期 ${expected_requests}。" >&2
    return 1
  fi
  account_status_file="$runtime_dir/account-status.json"
  account_access_file="$runtime_dir/account-access.json"
  account_prices_file="$runtime_dir/account-prices.json"
  account_events_file="$runtime_dir/account-events.json"
  account_call "$port" "/v0/resource/plugins/cpa-key-billing/status" >"$account_status_file"
  account_call "$port" "/v0/resource/plugins/cpa-key-billing/access" >"$account_access_file"
  account_call "$port" "/v0/resource/plugins/cpa-key-billing/prices" >"$account_prices_file"
  account_call "$port" "/v0/resource/plugins/cpa-key-billing/events?limit=100" >"$account_events_file"
  if ! jq -e '
      .tracked == true and
      (has("keys") | not) and (has("plans") | not) and (has("prices") | not)
    ' "$account_status_file" >/dev/null ||
    ! jq -e '
      (.models | length) == 0 and (.credentials | length) == 0 and
      .routing_valid == true and (.warnings | length) == 0 and
      (has("routing") | not) and (has("bindings") | not)
    ' "$account_access_file" >/dev/null ||
    ! jq -e 'length > 0 and all(.[]; has("pattern") and has("source") and (has("operation") | not))' \
      "$account_prices_file" >/dev/null ||
    ! jq -e --argjson expected "$expected_requests" '
      .total == $expected and (.entries | length) == $expected and
      all(.entries[]; .scope == "" and (has("auth_index") | not) and has("cost")) and
      any(.entries[]; has("executor_type")) and any(.entries[]; has("source"))
    ' "$account_events_file" >/dev/null; then
    echo "CLIProxyAPI ${host_label} 的 API Key 自助查询范围或响应字段不正确。" >&2
    return 1
  fi
  log_step "API Key 自助查询已验证：仅返回当前 Key 的 35 条请求事件"
  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/analysis" >"$runtime_dir/analysis.json"
  if ! jq -e '
      .usage_distribution.models as $models |
      [$models[] | select((.key | startswith("e2e-")) or (.key | startswith("codex/e2e-")))] as $matrix |
      ($matrix | length) == 32 and
      all($matrix[]; .requests == 1) and
      ([ $models[] | select(.key == "gpt-5.6-sol") | .requests ] | add) == 1 and
      ([ $models[] | select(.key == "codex/gpt-5.6-sol") | .requests ] | add) == 1 and
      ([ $models[] | select(.key == "gpt-auto") | .requests ] | add) == 1
    ' "$runtime_dir/analysis.json" >/dev/null; then
    echo "复杂模型路由的用量统计不正确。" >&2
    return 1
  fi
  log_step "聚合统计已验证：35 条基础计费记录"

  log_step "并发限制：SSE 占槽、HTTP 拦截与完成释放"
  assert_concurrency_limit "$port" "$runtime_dir" "$expected_requests"
  expected_requests=$((expected_requests + 1))
  log_step "路由模型规则：无绑定状态、3 次拦截与恢复"
  assert_route_model_policy "$port" "$runtime_dir" "$expected_requests"
  expected_requests=$((expected_requests + 1))
  log_step "路由凭证规则：整类与指定凭证均限制真实候选集"
  assert_route_credential_policy "$port" "$runtime_dir" "$expected_requests"
  expected_requests=$((expected_requests + 2))
  log_step "订阅额度：消费、4 种协议拦截与恢复"
  assert_quota_exhausted "$port" "$runtime_dir" "$expected_requests"

  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/plugin-logs" >"$runtime_dir/plugin-logs.json"
  if ! jq -e '[.entries[] | select(.level == "info" and (.message | contains("已加载计费数据库")))] | length == 1' \
    "$runtime_dir/plugin-logs.json" >/dev/null; then
    echo "插件日志缺少启动记录：$(jq -c '.entries' "$runtime_dir/plugin-logs.json")" >&2
    return 1
  fi
  log_step "插件启动事件已验证"

  kill "$active_pid" >/dev/null 2>&1 || true
  wait "$active_pid" >/dev/null 2>&1 || true
  active_pid=""
  log_ok "${host_label}：41 个上游请求，1 次并发拦截，3 次模型拦截，2 次凭证路由，1 次凭证拦截，4 次额度拦截"
}

log_stage "启动 dummy provider"
start_upstream
log_step "监听 127.0.0.1:${upstream_port}"

target_index=0
for target in "${targets[@]}"; do
  run_target "$target" "$target_index"
  target_index=$((target_index + 1))
done

if (( ${#targets[@]} > 1 )); then
  log_ok "全部 ${#targets[@]} 个目标通过"
fi
