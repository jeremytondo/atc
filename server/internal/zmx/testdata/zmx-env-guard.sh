#!/bin/sh

if [ "${ZMX_SESSION+set}" = "set" ]; then
	printf 'inherited ZMX_SESSION=%s\n' "$ZMX_SESSION" >&2
	exit 97
fi

case "${1:-}" in
attach)
	printf 'attached without parent ZMX_SESSION\n'
	;;
list)
	if [ -n "${ATC_TEST_ZMX_NAME:-}" ]; then
		printf 'name=%s\n' "$ATC_TEST_ZMX_NAME"
	fi
	;;
run | send | kill)
	;;
*)
	printf 'unexpected command: %s\n' "${1:-}" >&2
	exit 98
	;;
esac
