#!/usr/bin/env bash

set -euo pipefail

readonly github_repo="router-for-me/CLIProxyAPI"
readonly model="${DEEPSEEK_MODEL:-deepseek-v4-flash}"
readonly pool_model="${DEEPSEEK_POOL_MODEL:-deepseek-v4-pro}"
readonly chat_route="deepseek-chat-route"
readonly responses_route="deepseek-responses-route"
readonly anthropic_route="deepseek-anthropic-route"
readonly fixed_route="deepseek-fixed"
readonly pool_route="deepseek-pool"
readonly openai_base_url="${DEEPSEEK_OPENAI_BASE_URL:-https://api.deepseek.com}"
readonly anthropic_base_url="${DEEPSEEK_ANTHROPIC_BASE_URL:-https://api.deepseek.com/anthropic}"
readonly requested_versions="${CPA_E2E_VERSIONS:-7.2.103 7.2.123 latest}"
readonly base_port="${CPA_E2E_BASE_PORT:-28317}"

if [[ -z "${DEEPSEEK_API_KEY:-}" ]]; then
  echo "缺少 DEEPSEEK_API_KEY。" >&2
  exit 1
fi

for command_name in curl jq tar go; do
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

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ -n "$active_pid" ]]; then
    kill "$active_pid" >/dev/null 2>&1 || true
    wait "$active_pid" >/dev/null 2>&1 || true
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

mkdir -p "$cache_dir" "$run_dir/plugin"
plugin_path="$run_dir/plugin/cpa-key-billing.$plugin_extension"
echo "构建插件……"
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

resolved_versions=""
for requested_version in $requested_versions; do
  version="$(resolve_version "$requested_version")"
  case " $resolved_versions " in
    *" $version "*) ;;
    *) resolved_versions="$resolved_versions $version" ;;
  esac
done
resolved_versions="${resolved_versions# }"

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
    echo "下载 CLIProxyAPI v${version}……" >&2
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

wait_for_server() {
  local port="$1"
  local attempts=0
  while (( attempts < 120 )); do
    if curl -fsS --max-time 1 "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
      return
    fi
    if ! kill -0 "$active_pid" >/dev/null 2>&1; then
      echo "CLIProxyAPI 启动失败。" >&2
      return 1
    fi
    attempts=$((attempts + 1))
    sleep 0.5
  done
  echo "等待 CLIProxyAPI 启动超时。" >&2
  return 1
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

api_call() {
  local port="$1"
  local name="$2"
  local path="$3"
  local body="$4"
  local client_format="$5"
  local output="$6"
  local -a headers
  local http_status error_message

  headers=(-H "Content-Type: application/json")
  if [[ "$client_format" == "anthropic" ]]; then
    headers+=(-H "x-api-key: e2e-downstream-key" -H "anthropic-version: 2023-06-01")
  else
    headers+=(-H "Authorization: Bearer e2e-downstream-key")
  fi
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

extract_downstream_usage() {
  local client_format="$1"
  local stream="$2"
  local response_file="$3"
  if [[ "$stream" == "false" ]]; then
    case "$client_format" in
      chat)
        jq -er '[.usage.prompt_tokens, .usage.completion_tokens] | @tsv' "$response_file"
        ;;
      responses)
        jq -er '[.usage.input_tokens, .usage.output_tokens] | @tsv' "$response_file"
        ;;
      anthropic)
        jq -er '[
          (.usage.input_tokens + (.usage.cache_read_input_tokens // 0) + (.usage.cache_creation_input_tokens // 0)),
          .usage.output_tokens
        ] | @tsv' "$response_file"
        ;;
    esac
    return
  fi

  case "$client_format" in
    chat)
      jq -Rser '
        def events: split("\n")[] | sub("\r$"; "") | select(startswith("data:")) |
          sub("^data:[ ]?"; "") | select(. != "[DONE]") | fromjson?;
        [events | .usage? | select(type == "object")] | last as $usage |
        [$usage.prompt_tokens, $usage.completion_tokens] | @tsv
      ' "$response_file"
      ;;
    responses)
      jq -Rser '
        def events: split("\n")[] | sub("\r$"; "") | select(startswith("data:")) |
          sub("^data:[ ]?"; "") | select(. != "[DONE]") | fromjson?;
        [events | (.response.usage? // .usage?) | select(type == "object")] | last as $usage |
        [$usage.input_tokens, $usage.output_tokens] | @tsv
      ' "$response_file"
      ;;
    anthropic)
      jq -Rser '
        def events: split("\n")[] | sub("\r$"; "") | select(startswith("data:")) |
          sub("^data:[ ]?"; "") | select(. != "[DONE]") | fromjson?;
        reduce ([events | (.usage? // .message.usage?) | select(type == "object")][]) as $usage
          ({input: null, output: null, cache_read: 0, cache_write: 0};
            if ($usage | has("input_tokens")) then .input = $usage.input_tokens else . end |
            if ($usage | has("output_tokens")) then .output = $usage.output_tokens else . end |
            if ($usage | has("cache_read_input_tokens")) then .cache_read = $usage.cache_read_input_tokens else . end |
            if ($usage | has("cache_creation_input_tokens")) then .cache_write = $usage.cache_creation_input_tokens else . end
          ) |
        [(.input + .cache_read + .cache_write), .output] | @tsv
      ' "$response_file"
      ;;
  esac
}

request_body() {
  local client="$1"
  local requested_model="$2"
  local stream="$3"
  local prompt="$4"
  case "$client" in
    chat)
      jq -nc --arg model "$requested_model" --arg prompt "$prompt" --argjson stream "$stream" '
        {model: $model, messages: [{role: "user", content: $prompt}], max_tokens: 16, stream: $stream} +
        (if $stream then {stream_options: {include_usage: true}} else {} end)
      '
      ;;
    responses)
      jq -nc --arg model "$requested_model" --arg prompt "$prompt" --argjson stream "$stream" '
        {
          model: $model,
          input: [{type: "message", role: "user", content: [{type: "input_text", text: $prompt}]}],
          max_output_tokens: 16,
          stream: $stream
        }
      '
      ;;
    anthropic)
      jq -nc --arg model "$requested_model" --arg prompt "$prompt" --argjson stream "$stream" '
        {model: $model, messages: [{role: "user", content: $prompt}], max_tokens: 16, stream: $stream}
      '
      ;;
  esac
}

assert_latest_entry() {
  local port="$1"
  local expected_count="$2"
  local client="$3"
  local upstream="$4"
  local billing_model="$5"
  local upstream_models="$6"
  local logs_file="$7"
  local response_file="$8"
  local stream="$9"
  local expected_endpoint expected_source attempt actual_count usage input output billed_input billed_output

  case "$client" in
    chat) expected_endpoint="/v1/chat/completions" ;;
    responses) expected_endpoint="/v1/responses" ;;
    anthropic) expected_endpoint="/v1/messages" ;;
  esac
  # Every upstream here is a provider configured in config.yaml, so its source
  # is the provider and the masked API key CLIProxyAPI reports for it. All three
  # are the same key, which is what makes the provider the part that separates
  # them.
  case "$upstream" in
    chat) expected_source="deepseek-chat-e2e" ;;
    responses) expected_source="codex" ;;
    anthropic) expected_source="claude" ;;
  esac
  expected_source+=" · ${DEEPSEEK_API_KEY:0:6}…${DEEPSEEK_API_KEY: -4}"

  attempt=0
  while (( attempt < 20 )); do
    management_call GET "$port" "/v0/management/plugins/cpa-key-billing/logs?limit=100" >"$logs_file"
    actual_count="$(jq -er '.entries | length' "$logs_file")"
    if [[ "$actual_count" == "$expected_count" ]]; then
      break
    fi
    attempt=$((attempt + 1))
    sleep 0.1
  done
  if [[ "$actual_count" != "$expected_count" ]]; then
    echo "计费日志数量为 ${actual_count}，预期 ${expected_count}。" >&2
    return 1
  fi
  if ! jq -e \
    --arg upstream_models "$upstream_models" \
    --arg billing_model "$billing_model" \
    --arg endpoint "$expected_endpoint" \
    --arg source "$expected_source" '
      .entries[0] |
      (.upstream_model as $actual | ($upstream_models | split(",") | index($actual)) != null) and
      .billing_model == $billing_model and
      .endpoint == $endpoint and
      .source == $source and
      .accounting_quality == "complete" and
      .price_source == "override" and
      ((.cost.uncached_input_tokens + .cost.cache_read_tokens + .cost.cache_write_tokens) > 0) and
      (.cost.billed_output_tokens > 0) and
      (.cost.total_usd > 0)
    ' "$logs_file" >/dev/null; then
    echo "最新计费日志的端点、来源、usage 或定价不正确。" >&2
    return 1
  fi

  if [[ "$client" != "$upstream" ]]; then
    return
  fi
  usage="$(extract_downstream_usage "$client" "$stream" "$response_file")"
  IFS=$'\t' read -r input output <<<"$usage"
  billed_input="$(jq -er '.entries[0].cost | .uncached_input_tokens + .cache_read_tokens + .cache_write_tokens' "$logs_file")"
  billed_output="$(jq -er '.entries[0].cost.billed_output_tokens' "$logs_file")"
  if [[ "$input" != "$billed_input" || "$output" != "$billed_output" ]]; then
    echo "${client} 直通响应与计费日志不一致。" >&2
    return 1
  fi
}

# assert_model_access grants the downstream key a group that does not hold the
# route this suite calls, and asserts the request is refused before it can reach
# an upstream: 403 in the client's own error shape, nothing in the billing log,
# and one line in the plugin log, which is the only record such a request leaves.
assert_model_access() {
  local port="$1"
  local runtime_dir="$2"
  local expected_count="$3"
  local scope group body http_status response_file logs_file events_file

  response_file="$runtime_dir/responses/model-blocked.json"
  logs_file="$runtime_dir/model-access-logs.json"
  events_file="$runtime_dir/model-access-events.json"

  # Synchronize the Key list the way the panel does, so this holds whether or
  # not traffic has already created the record.
  management_call POST "$port" "/v0/management/plugins/cpa-key-billing/keys/sync" \
    -H "Content-Type: application/json" \
    --data '{"keys":["e2e-downstream-key"]}' \
    >/dev/null
  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/overview" >"$runtime_dir/overview.json"
  scope="$(jq -er 'first(.keys[] | select(.in_config) | .scope)' "$runtime_dir/overview.json")"
  if ! jq -e --arg scope "$scope" 'first(.keys[] | select(.scope == $scope)) | .all_models' \
    "$runtime_dir/overview.json" >/dev/null; then
    echo "API Key 的默认可用模型不是全部模型。" >&2
    return 1
  fi

  management_call POST "$port" "/v0/management/plugins/cpa-key-billing/model-groups" \
    -H "Content-Type: application/json" \
    --data "{\"name\":\"e2e-限定分组\",\"models\":[\"$responses_route\"]}" \
    >"$runtime_dir/model-group.json"
  group="$(jq -er '.model_group.id' "$runtime_dir/model-group.json")"
  management_call POST "$port" "/v0/management/plugins/cpa-key-billing/keys/models" \
    -H "Content-Type: application/json" \
    --data "$(jq -nc --arg scope "$scope" --arg group "$group" '{scope: $scope, groups: [$group], models: []}')" \
    >/dev/null

  # A thinking suffix is a request option rather than a model of its own, so it
  # is no way around the refusal — and the refusal names the model without it,
  # which is the name the billing log would have carried.
  for requested in "chat/$chat_route" "chat/$chat_route(high)" "chat/$chat_route(max)"; do
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
    if ! jq -e --arg model "chat/$chat_route" '
        .error.type == "permission_error" and .error.code == "insufficient_quota"
        and (.error.message | contains("\"" + $model + "\""))' "$response_file" >/dev/null; then
      echo "模型拦截 ${requested} 的错误内容不正确：$(jq -c '.' "$response_file")" >&2
      return 1
    fi
  done

  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/logs?limit=100" >"$logs_file"
  if [[ "$(jq -er '.entries | length' "$logs_file")" != "$expected_count" ]]; then
    echo "被拦截的请求进入了计费日志。" >&2
    return 1
  fi
  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/events" >"$events_file"
  if ! jq -e --arg model "chat/$chat_route" '
      [.events[] | select(.level == "info" and (.message | startswith("模型拦截：")) and (.message | contains($model)))]
      | length == 1' "$events_file" >/dev/null; then
    echo "插件日志缺少模型拦截记录：$(jq -c '.events' "$events_file")" >&2
    return 1
  fi

  # Back to every model, which is what selecting the all-models group means: the
  # request that follows has to be billed exactly like any other.
  management_call POST "$port" "/v0/management/plugins/cpa-key-billing/keys/models" \
    -H "Content-Type: application/json" \
    --data "$(jq -nc --arg scope "$scope" '{scope: $scope, groups: ["all"], models: []}')" \
    >/dev/null
  management_call DELETE "$port" "/v0/management/plugins/cpa-key-billing/model-groups?id=$group" >/dev/null

  body="$(request_body chat "chat/$chat_route" false "Reply with exactly OK.")"
  api_call "$port" "模型拦截解除后：OpenAI Chat → OpenAI Chat 非流式" \
    "/v1/chat/completions" "$body" chat "$runtime_dir/responses/model-restored.json"
  assert_latest_entry "$port" "$((expected_count + 1))" chat chat \
    "chat/$chat_route" "$model" "$runtime_dir/model-restored-logs.json" \
    "$runtime_dir/responses/model-restored.json" false
}

# assert_canceled_request disconnects a client mid-generation, which is the one
# outcome the plugin cannot observe from usage alone: the provider reports
# nothing, so without the terminal event the request would vanish from the log.
assert_canceled_request() {
  local port="$1"
  local runtime_dir="$2"
  local body curl_pid attempt logs_file
  logs_file="$runtime_dir/canceled-billing.json"
  body="$(jq -nc --arg model "chat/$chat_route" '
    {model: $model, max_tokens: 2048, stream: true,
     messages: [{role: "user", content: "Count from 1 to 500, one number per line."}]}')"

  curl -sS -N --max-time 30 \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer e2e-downstream-key" \
    --data "$body" \
    --output "$runtime_dir/responses/canceled.sse" \
    "http://127.0.0.1:$port/v1/chat/completions" >/dev/null 2>&1 &
  curl_pid=$!
  sleep 1
  kill -9 "$curl_pid" >/dev/null 2>&1 || true
  wait "$curl_pid" >/dev/null 2>&1 || true

  attempt=0
  while (( attempt < 50 )); do
    management_call GET "$port" "/v0/management/plugins/cpa-key-billing/logs?limit=1" >"$logs_file"
    if jq -e '.entries[0].outcome == "canceled"' "$logs_file" >/dev/null 2>&1; then
      return 0
    fi
    attempt=$((attempt + 1))
    sleep 0.2
  done
  echo "断开的流式请求没有记入计费日志：$(jq -c '.entries[0]' "$logs_file")" >&2
  return 1
}

run_version() {
  local version="$1"
  local index="$2"
  local port=$((base_port + index))
  local version_dir="$run_dir/$version"
  local host_dir="$version_dir/host"
  local runtime_dir="$version_dir/runtime"
  local archive host_binary api_key_json plugins_file prompt
  local client upstream stream prefix endpoint body mode extension response_file logs_file
  local client_label upstream_label mode_label request_number route_model billing_model upstream_models
  local request_index expected_requests actual_requests
  local -a model_cases
  mkdir -p "$host_dir" "$runtime_dir/plugins" "$runtime_dir/auth" "$runtime_dir/responses"

  archive="$(download_host "$version")"
  tar -xzf "$archive" -C "$host_dir"
  host_binary="$(find "$host_dir" -type f -name 'cli-proxy-api' -perm -111 | head -n 1)"
  if [[ -z "$host_binary" ]]; then
    echo "CLIProxyAPI v$version 发布包中缺少可执行文件。" >&2
    return 1
  fi
  cp "$plugin_path" "$runtime_dir/plugins/cpa-key-billing.$plugin_extension"

  api_key_json="$(printf '%s' "$DEEPSEEK_API_KEY" | jq -Rs .)"
  cat >"$runtime_dir/config.yaml" <<EOF
host: "127.0.0.1"
port: $port
auth-dir: "$runtime_dir/auth"
api-keys:
  - "e2e-downstream-key"
logging-to-file: false
remote-management:
  allow-remote: false
  secret-key: "e2e-management-key"
  disable-control-panel: true
plugins:
  enabled: true
  dir: "$runtime_dir/plugins"
  configs:
    cpa-key-billing:
      enabled: true
      state_file: "$runtime_dir/state.db"
openai-compatibility:
  - name: "deepseek-direct-e2e"
    base-url: "$openai_base_url"
    api-key-entries:
      - api-key: $api_key_json
    models:
      - name: "$model"
        alias: "$model"
  - name: "deepseek-chat-e2e"
    prefix: "chat"
    base-url: "$openai_base_url"
    api-key-entries:
      - api-key: $api_key_json
    models:
      - name: "$model"
        alias: "$model"
      - name: "$model"
        alias: "$chat_route"
      - name: "$model(high)"
        alias: "$fixed_route"
      - name: "$model"
        alias: "$pool_route"
      - name: "$pool_model"
        alias: "$pool_route"
codex-api-key:
  - api-key: $api_key_json
    prefix: "responses"
    base-url: "$openai_base_url"
    models:
      - name: "$model"
        alias: "$responses_route"
        is-compat: true
claude-api-key:
  - api-key: $api_key_json
    prefix: "anthropic"
    base-url: "$anthropic_base_url"
    models:
      - name: "$model"
        alias: "$anthropic_route"
        is-compat: true
    cloak:
      mode: "never"
EOF
  chmod 600 "$runtime_dir/config.yaml"

  echo "测试 CLIProxyAPI v${version}（端口 ${port}）……"
  "$host_binary" -config "$runtime_dir/config.yaml" -local-model >"$runtime_dir/host.log" 2>&1 &
  active_pid=$!
  if ! wait_for_server "$port"; then
    tail -n 80 "$runtime_dir/host.log" >&2 || true
    return 1
  fi

  plugins_file="$runtime_dir/plugins.json"
  management_call GET "$port" "/v0/management/plugins" >"$plugins_file"
  if ! jq -e '.plugins[] | select(.id == "cpa-key-billing" and .registered == true and .effective_enabled == true)' "$plugins_file" >/dev/null; then
    echo "插件未在 CLIProxyAPI v$version 中注册。" >&2
    tail -n 80 "$runtime_dir/host.log" >&2 || true
    return 1
  fi

  management_call PUT "$port" "/v0/management/plugins/cpa-key-billing/prices" \
    -H "Content-Type: application/json" \
    --data "{\"pattern\":\"$model\",\"input_per_1m\":1,\"output_per_1m\":2,\"cache_read_per_1m\":0.1,\"cache_write_per_1m\":1.25}" \
    >"$runtime_dir/price.json"
  for priced_model in \
    "$chat_route" "chat/$chat_route" "chat/$model" \
    "$responses_route" "responses/$responses_route" \
    "$anthropic_route" "anthropic/$anthropic_route" \
    "chat/$fixed_route" "chat/$pool_route"; do
    management_call PUT "$port" "/v0/management/plugins/cpa-key-billing/prices" \
      -H "Content-Type: application/json" \
      --data "{\"pattern\":\"$priced_model\",\"input_per_1m\":1,\"output_per_1m\":2,\"cache_read_per_1m\":0.1,\"cache_write_per_1m\":1.25}" \
      >"$runtime_dir/price.json"
  done

  prompt="Reply with exactly OK."
  request_index=0
  for client in chat responses anthropic; do
    case "$client" in
      chat) client_label="OpenAI Chat"; endpoint="/v1/chat/completions" ;;
      responses) client_label="OpenAI Responses"; endpoint="/v1/responses" ;;
      anthropic) client_label="Anthropic Messages"; endpoint="/v1/messages" ;;
    esac
    for upstream in chat responses anthropic; do
      case "$upstream" in
        chat) upstream_label="OpenAI Chat"; prefix="chat"; route_model="$prefix/$chat_route" ;;
        responses) upstream_label="OpenAI Responses"; prefix="responses"; route_model="$prefix/$responses_route" ;;
        anthropic) upstream_label="Anthropic Messages"; prefix="anthropic"; route_model="$prefix/$anthropic_route" ;;
      esac
      billing_model="$route_model"
      upstream_models="$model"
      for stream in false true; do
        request_index=$((request_index + 1))
        if [[ "$stream" == "true" ]]; then
          mode="stream"; mode_label="流式"; extension="sse"
        else
          mode="nonstream"; mode_label="非流式"; extension="json"
        fi
        body="$(request_body "$client" "$route_model" "$stream" "$prompt")"
        printf -v request_number '%02d' "$request_index"
        response_file="$runtime_dir/responses/${request_number}-${client}-to-${upstream}-${mode}.${extension}"
        logs_file="$runtime_dir/responses/${request_number}-billing.json"
        api_call "$port" "${client_label} → ${upstream_label} ${mode_label}" \
          "$endpoint" "$body" "$client" "$response_file"
        assert_latest_entry "$port" "$request_index" "$client" "$upstream" \
          "$billing_model" "$upstream_models" "$logs_file" "$response_file" "$stream"
      done
    done
  done

  model_cases=(
    "推理强度|chat|chat|false|chat/$model(high)|chat/$model|$model"
    "配置模型|responses|chat|false|$chat_route|$chat_route|$model"
    "组合路由|anthropic|responses|true|responses/$responses_route(high)|responses/$responses_route|$model"
    "固定后缀模型|chat|chat|false|chat/$fixed_route|chat/$fixed_route|$model"
    "模型池|responses|chat|false|chat/$pool_route|chat/$pool_route|$model,$pool_model"
  )
  for model_case in "${model_cases[@]}"; do
    IFS='|' read -r case_name client upstream stream route_model billing_model upstream_models <<<"$model_case"
    case "$client" in
      chat) client_label="OpenAI Chat"; endpoint="/v1/chat/completions" ;;
      responses) client_label="OpenAI Responses"; endpoint="/v1/responses" ;;
      anthropic) client_label="Anthropic Messages"; endpoint="/v1/messages" ;;
    esac
    case "$upstream" in
      chat) upstream_label="OpenAI Chat" ;;
      responses) upstream_label="OpenAI Responses" ;;
      anthropic) upstream_label="Anthropic Messages" ;;
    esac
    request_index=$((request_index + 1))
    if [[ "$stream" == "true" ]]; then
      mode="stream"; mode_label="流式"; extension="sse"
    else
      mode="nonstream"; mode_label="非流式"; extension="json"
    fi
    body="$(request_body "$client" "$route_model" "$stream" "$prompt")"
    printf -v request_number '%02d' "$request_index"
    response_file="$runtime_dir/responses/${request_number}-${case_name}-${mode}.${extension}"
    logs_file="$runtime_dir/responses/${request_number}-billing.json"
    api_call "$port" "${case_name}：${client_label} → ${upstream_label} ${mode_label}" \
      "$endpoint" "$body" "$client" "$response_file"
    assert_latest_entry "$port" "$request_index" "$client" "$upstream" \
      "$billing_model" "$upstream_models" "$logs_file" "$response_file" "$stream"
  done

  logs_file="$runtime_dir/logs.json"
  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/logs?limit=100" >"$logs_file"
  expected_requests=23
  actual_requests="$(jq -er '.entries | length' "$logs_file")"
  if [[ "$actual_requests" != "$expected_requests" ]]; then
    echo "CLIProxyAPI v${version} 计费日志数量为 ${actual_requests}，预期 ${expected_requests}。" >&2
    return 1
  fi
  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/stats" >"$runtime_dir/stats.json"
  if ! jq -e \
    --arg chat "chat/$chat_route" \
    --arg responses "responses/$responses_route" \
    --arg anthropic "anthropic/$anthropic_route" '
      .by_model as $models |
      ([ $models[] | select(.billing_model == $chat) | .requests ] | add) == 6 and
      ([ $models[] | select(.billing_model == $responses) | .requests ] | add) == 7 and
      ([ $models[] | select(.billing_model == $anthropic) | .requests ] | add) == 6
    ' "$runtime_dir/stats.json" >/dev/null; then
    echo "复杂模型路由的用量统计不正确。" >&2
    return 1
  fi

  assert_model_access "$port" "$runtime_dir" "$expected_requests"
  assert_canceled_request "$port" "$runtime_dir"

  management_call GET "$port" "/v0/management/plugins/cpa-key-billing/events" >"$runtime_dir/events.json"
  if ! jq -e '[.events[] | select(.level == "info" and (.message | contains("已加载计费数据库")))] | length == 1' \
    "$runtime_dir/events.json" >/dev/null; then
    echo "插件日志缺少启动记录：$(jq -c '.events' "$runtime_dir/events.json")" >&2
    return 1
  fi

  kill "$active_pid" >/dev/null 2>&1 || true
  wait "$active_pid" >/dev/null 2>&1 || true
  active_pid=""
  echo "CLIProxyAPI v${version}：通过（24 个真实上游请求，另有 1 次模型拦截和 1 次客户端中途断开）。"
}

version_index=0
for version in $resolved_versions; do
  run_version "$version" "$version_index"
  version_index=$((version_index + 1))
done

echo "全部通过：$resolved_versions"
