#!/usr/bin/env bash
# One-command launcher for the unified-core prototype. All provider credentials,
# core state, logs, and zmx sessions stay inside this experiment's .state tree.
set -Eeuo pipefail

experiment_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
runtime_dir="${ATC_UNIFIED_RUNTIME_DIR:-${experiment_dir}/.state}"
core_state_dir="${runtime_dir}/core"
codex_home_dir="${runtime_dir}/codex-home"
claude_home_dir="${runtime_dir}/claude-home"
log_dir="${runtime_dir}/logs"
core_address="${ATC_UNIFIED_ADDRESS:-127.0.0.1:7332}"
codex_address="${ATC_UNIFIED_CODEX_ADDRESS:-127.0.0.1:7444}"
core_url="http://${core_address}"
codex_url="ws://${codex_address}"
core_pid=""
codex_pid=""

export CODEX_HOME="${codex_home_dir}"
export CLAUDE_CONFIG_DIR="${claude_home_dir}"

fail() {
	printf 'atc-unified play: %s\n' "$*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "missing required command '$1'"
}

stop_process() {
	local pid="$1"
	if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
		kill "${pid}" 2>/dev/null || true
		wait "${pid}" 2>/dev/null || true
	fi
}

cleanup() {
	trap - EXIT INT TERM
	stop_process "${core_pid}"
	stop_process "${codex_pid}"
}

wait_for_tcp() {
	local address="$1"
	local pid="$2"
	local name="$3"
	local log="$4"
	local host="${address%:*}"
	local port="${address##*:}"
	local attempt
	for attempt in {1..100}; do
		if ! kill -0 "${pid}" 2>/dev/null; then
			printf '%s failed to start. Log follows:\n' "${name}" >&2
			tail -40 "${log}" >&2 || true
			return 1
		fi
		if (exec 3<>"/dev/tcp/${host}/${port}") 2>/dev/null; then
			return 0
		fi
		sleep 0.05
	done
	printf '%s did not become ready. Log follows:\n' "${name}" >&2
	tail -40 "${log}" >&2 || true
	return 1
}

login_if_needed() {
	if ! codex login status >/dev/null 2>&1; then
		if [[ -n "${OPENAI_API_KEY:-}" ]]; then
			printf '%s' "${OPENAI_API_KEY}" | codex login --with-api-key
		else
			printf '\nCodex needs a one-time device login for this isolated prototype profile.\n\n'
			codex login --device-auth
		fi
	fi
	if [[ -z "${ANTHROPIC_API_KEY:-}" ]] && ! claude auth status >/dev/null 2>&1; then
		printf '\nClaude needs a one-time login for this isolated prototype profile.\n\n'
		claude auth login
	fi
}

for command in go codex claude zmx; do
	require_command "${command}"
done

mkdir -p "${core_state_dir}" "${codex_home_dir}" "${claude_home_dir}" "${log_dir}"
if [[ "${ATC_UNIFIED_SKIP_LOGIN:-0}" != "1" ]]; then
	login_if_needed
fi

printf 'Building atc-unified…\n'
make -s -C "${experiment_dir}" build

trap cleanup EXIT INT TERM

printf 'Starting private Codex app-server at %s…\n' "${codex_url}"
codex app-server --listen "${codex_url}" >"${log_dir}/codex-app-server.log" 2>&1 &
codex_pid=$!
wait_for_tcp "${codex_address}" "${codex_pid}" "Codex app-server" "${log_dir}/codex-app-server.log" || exit 1

printf 'Starting unified core at %s…\n' "${core_url}"
"${experiment_dir}/bin/atc-unified" serve \
	--listen "${core_address}" \
	--state "${core_state_dir}" \
	--codex-remote "${codex_url}" \
	--claude-model "${ATC_UNIFIED_CLAUDE_MODEL:-haiku}" \
	--codex-model "${ATC_UNIFIED_CODEX_MODEL:-gpt-5.6-luna}" \
	--effort "${ATC_UNIFIED_EFFORT:-low}" \
	--debug >"${log_dir}/core.log" 2>&1 &
core_pid=$!
wait_for_tcp "${core_address}" "${core_pid}" "Unified core" "${log_dir}/core.log" || exit 1

printf 'Opening play. Logs are in %s.\n' "${log_dir}"
"${experiment_dir}/bin/atc-unified" play --base "${core_url}"
