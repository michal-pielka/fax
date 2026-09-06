#!/usr/bin/env bash
#
# Read the running stack's logs.
#
# Everything the Go services emit is JSON, so these are jq queries. The one
# awkward part is that `docker compose logs` prefixes each line with the
# service name, which is not JSON -- stripping it would lose which service
# spoke, so it gets folded into the object as "svc" instead.
#
#   ./deploy/logs.sh trace <id>    one receipt, everywhere it went
#   ./deploy/logs.sh prints        what people have printed
#   ./deploy/logs.sh errors        warnings and errors only
#   ./deploy/logs.sh slow [ms]     requests over a threshold, default 1000
#   ./deploy/logs.sh callers       who is using it, busiest first
#   ./deploy/logs.sh printer       what the device has said about itself
#   ./deploy/logs.sh tail          live, readable
#   ./deploy/logs.sh raw           the JSON stream, for your own jq
#
# All of them take --since, passed straight to docker:
#
#   ./deploy/logs.sh --since 10m errors
#
# To work on a saved dump instead of the live stack:
#
#   FAX_LOGS=/tmp/incident.log ./deploy/logs.sh trace 9ca16fa7

set -euo pipefail

SINCE=""
if [[ "${1:-}" == "--since" || "${1:-}" == "-s" ]]; then
	SINCE="$2"
	shift 2
fi

# Lines the Go services and Caddy emit are JSON; mosquitto's are not, and
# `fromjson? // empty` is what quietly drops those rather than failing.
stream() {
	if [[ -n "${FAX_LOGS:-}" ]]; then
		cat "$FAX_LOGS"
	elif [[ -n "$SINCE" ]]; then
		docker compose logs --no-color --since "$SINCE" "$@"
	else
		docker compose logs --no-color "$@"
	fi |
		sed -E 's/^([^ |]+)[[:space:]]*\|[[:space:]]*\{/{"svc":"\1",/' |
		jq -R 'fromjson? // empty'
}

# Service names are padded to align; trimming makes the columns line up again.
SVC='(.svc // "caddy") | sub("-[0-9]+$"; "") | .[0:10]'

case "${1:-help}" in
trace)
	[[ -n "${2:-}" ]] || { echo "usage: logs.sh trace <id>" >&2; exit 2; }
	# Everything carrying this id, in the order it happened. The one query
	# that answers "what happened to this receipt".
	stream | jq -r --arg id "$2" '
		select((.trace // "") | startswith($id))
		| "\(.time[11:19])  \('"$SVC"' | . + " " * (10 - length))  \(.msg)  \(
			del(.time, .level, .msg, .svc, .trace) | to_entries
			| map("\(.key)=\(.value)") | join(" "))"'
	;;
prints)
	# The record of what has actually been sent to the paper: logged only
	# after the firmware acknowledged the job.
	stream | jq -r 'select(.msg == "printed")
		| "\(.time[0:19] | sub("T"; " "))  \(.trace[0:8])  \(.chars)c  \(.text)"'
	;;
errors)
	stream | jq -r 'select(.level == "WARN" or .level == "ERROR")
		| "\(.time[11:19])  \(.level)  \('"$SVC"')  \(.msg)  \(
			del(.time, .level, .msg, .svc) | to_entries
			| map("\(.key)=\(.value)") | join(" "))"'
	;;
slow)
	# A print legitimately takes about a second: it waits for the firmware's
	# ack. Raise the threshold to see only the ones that waited longer.
	stream | jq -r --argjson over "${2:-1000}" '
		select(.msg == "request" and .ms >= $over)
		| "\(.time[11:19])  \(.ms)ms  \(.status)  \(.method) \(.path)  \(.ip)  \(.trace[0:8])"'
	;;
callers)
	# Only the public surface: the gateway calling the renderer is not a
	# caller, and internal container addresses just crowd the list.
	stream | jq -sr '[.[] | select(.msg == "request" and .ip and (.path | startswith("/api/")))]
		| group_by(.ip) | map({ip: .[0].ip, hits: length,
			prints: [.[] | select(.path == "/api/print")] | length})
		| sort_by(-.hits)[]
		| "\(.hits | tostring | (" " * (6 - length)) + .)  \(.prints | tostring
			| (" " * (6 - length)) + .) prints  \(.ip)"'
	;;
printer)
	# The device's own account of itself, plus the jobs it acknowledged.
	stream | jq -r 'select(.msg | test("device state|device ack|printer released|no acknowledgement|broker connection|connected to broker"))
		| "\(.time[11:19])  \(.msg)  \(
			del(.time, .level, .msg, .svc) | to_entries
			| map("\(.key)=\(.value)") | join(" "))"'
	;;
tail)
	docker compose logs --no-color -f --tail 20 |
		sed -E 's/^([^ |]+)[[:space:]]*\|[[:space:]]*\{/{"svc":"\1",/' |
		jq -Rr 'fromjson? // empty
			| "\(.time[11:19] // "        ")  \(.level // "INFO" | .[0:4])  \('"$SVC"')  \(.msg)  \(
				del(.time, .level, .msg, .svc) | to_entries
				| map("\(.key)=\(.value)") | join(" "))"'
	;;
raw)
	stream
	;;
*)
	sed -n '3,30p' "$0" | sed 's/^# \{0,1\}//'
	;;
esac
