#!/usr/bin/env bash
#
# Take a print off the wall at /prints, for good. The id is in the print's
# link (/prints#<id>) and in the logs.
#
#   ./deploy/hide.sh <id>
#
# The gateway serves this route only inside the compose network, so it is
# called from the caddy container, which has wget.

set -euo pipefail

[[ -n "${1:-}" ]] || { echo "usage: hide.sh <id>" >&2; exit 2; }
[[ "$1" =~ ^[A-Za-z0-9-]+$ ]] || { echo "not a print id: $1" >&2; exit 2; }

cd "$(dirname "$0")/.."

docker compose exec -T caddy wget -qO- --post-data= "http://gateway:8080/internal/prints/$1/hide"
echo
