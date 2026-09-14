#!/usr/bin/env bash
#
# Deploy what is on main. Nothing is compiled here: the images were built by
# GitHub Actions when main was pushed, and this pulls them.
#
#   ./deploy/update.sh              newest of everything
#   FAX_TAG=<sha> ./deploy/update.sh   a specific build, e.g. to roll back

set -euo pipefail

cd "$(dirname "$0")/.."

# The checkout matters too: the frontend, the Caddyfile and the Mosquitto
# config are mounted from it, not baked into images.
before=$(git rev-parse HEAD)
git pull --ff-only
after=$(git rev-parse HEAD)

docker compose pull --quiet
docker compose up -d --remove-orphans

# Neither of these reads its config while running. Compose does not restart a
# container whose only change is a mounted file, so it has to be done here,
# and only when the file actually changed: a Mosquitto restart drops every
# client for a few seconds.
if ! git diff --quiet "$before" "$after" -- deploy/mosquitto/; then
	echo "mosquitto config changed; restarting the broker"
	docker compose restart mosquitto
fi

if ! git diff --quiet "$before" "$after" -- deploy/Caddyfile; then
	echo "Caddyfile changed; reloading caddy"
	docker compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile
fi

docker compose ps --format "table {{.Service}}\t{{.Status}}\t{{.Image}}"
