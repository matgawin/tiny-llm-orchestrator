#!/bin/sh
set -eu

tmp="${TMPDIR:-/tmp}/orc-runtime-recorder.$$"
record=""
prompt_file=""
idx=0

: > "$tmp"
for arg do
	printf 'arg:%s=%s\n' "$idx" "$arg" >> "$tmp"
	if [ "$arg" = "--record" ]; then
		record_next=1
	elif [ "${record_next:-0}" = "1" ]; then
		record="$arg"
		record_next=0
	fi
	if [ "$arg" = "--prompt-file" ]; then
		prompt_next=1
	elif [ "${prompt_next:-0}" = "1" ]; then
		prompt_file="$arg"
		prompt_next=0
	fi
	idx=$((idx + 1))
done

if [ -z "$record" ]; then
	echo "missing --record" >&2
	exit 64
fi

{
	printf 'cwd=%s\n' "$(pwd)"
	printf 'env:ORC_RUN_ID=%s\n' "${ORC_RUN_ID:-}"
	printf 'env:ORC_STEP_ID=%s\n' "${ORC_STEP_ID:-}"
	printf 'env:ORC_ATTEMPT_ID=%s\n' "${ORC_ATTEMPT_ID:-}"
	printf 'env:ORC_PROGRESS_SOCKET=%s\n' "${ORC_PROGRESS_SOCKET:-}"
	printf 'env:ORC_PROGRESS_TOKEN=%s\n' "${ORC_PROGRESS_TOKEN:-}"
	if [ -n "$prompt_file" ]; then
		printf 'prompt_file=%s\n' "$prompt_file"
		sed 's/^/prompt_file_content:/' "$prompt_file"
	else
		sed 's/^/stdin:/' -
	fi
} >> "$tmp"

mkdir -p "$(dirname "$record")"
mv "$tmp" "$record"
