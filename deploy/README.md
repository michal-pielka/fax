# Deploying

Nothing is compiled on the VPS. GitHub Actions builds the three service
images on every push to `main` (`.github/workflows/images.yml`) and pushes
them to the GitHub Container Registry; the VPS pulls them.

## One-time setup on the VPS

The repository is private, so the registry needs a login. Make a classic
personal access token with only the `read:packages` scope, then:

```sh
echo "<token>" | docker login ghcr.io -u michal-pielka --password-stdin
```

Docker keeps the credential in `~/.docker/config.json`; this is not repeated.

## Every deploy

Push to `main`, wait for the `images` workflow to go green (two to three
minutes), then on the VPS:

```sh
cd fax && ./deploy/update.sh
```

It pulls the checkout (frontend, Caddyfile and Mosquitto config are mounted
from it, not baked into images), pulls the images, restarts what changed,
and restarts Mosquitto or reloads Caddy only if their config changed.

## Rolling back

Every image is also tagged with the commit that built it:

```sh
FAX_TAG=<commit sha> ./deploy/update.sh
```

Put `FAX_TAG=` in `.env` to make a pin stick across deploys.

## Building locally instead

`docker compose up -d --build` still compiles here from the Dockerfiles.
That is for development on a laptop; never pass `--build` on the VPS.

## The firmware

Flashed from a machine with the printer's USB cable, never from the VPS:

```sh
cd firmware
WIFI_SSID=... WIFI_PASS=... MQTT_PASS=<printer password> cargo run --release
```

Flash and deploy together when a change touches both: the two ends share
the MQTT message shapes and the timeouts.
