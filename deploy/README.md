# Deploying

Nothing is compiled on the VPS. GitHub Actions builds the three service
images on every push to `main` (`.github/workflows/images.yml`) and pushes
them to the GitHub Container Registry; the VPS pulls them.

## One-time setup on the VPS

Secrets live in `.env` and the broker's password file, neither committed:

```sh
cp .env.example .env   # set MQTT_PASSWORD to something long

# -c creates the file, so only on the first account.
docker run --rm -v ./deploy/mosquitto:/m eclipse-mosquitto:2 \
  mosquitto_passwd -c -b /m/passwd backend '<MQTT_PASSWORD from .env>'
docker run --rm -v ./deploy/mosquitto:/m eclipse-mosquitto:2 \
  mosquitto_passwd -b /m/passwd printer '<a different password>'
```

The printer password is the one compiled into the firmware. Keep it apart
from the backend's: the ACL only lets `printer` receive jobs, never send
them, and that is only worth something if the two accounts differ.

GHCR packages are private unless made public in the package settings, even
when the repository is public. For private packages, the VPS needs a login.
Make a classic personal access token with only the `read:packages` scope,
then:

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

## The wall at /prints

Every print the printer confirms is appended to `prints.jsonl` in the `wall`
volume and pushed live to anyone watching `/prints`. Refused or unconfirmed
prints never appear.

Take one down (the id is in its link, `/prints#<id>`):

```sh
./deploy/hide.sh <id>
```

Backfilling old prints reads the gateway's logs. Docker keeps logs per
container, and a deploy that pulls a new gateway image replaces the
container, so save them first:

```sh
docker compose logs --no-color gateway > ~/gateway-logs.txt   # before update.sh
./deploy/update.sh
docker compose stop gateway
docker compose run --rm -T --no-deps gateway \
  -wall-dir=/var/lib/fax/wall -photos-dir=/var/lib/fax/photos -backfill < ~/gateway-logs.txt
docker compose start gateway
```

The gateway is stopped because backfill rewrites the file it appends to.
Logs carry no styles, so backfilled text comes back plain. A photo is
included only if its log line survived. Running it twice adds nothing twice.

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

Built for the ESP32-S3, which hosts the printer over its USB port; the
classic ESP32 and the 9600 baud serial header are history (see the `ttl-host`
branch's log for why). Flashed through the board's COM port from a machine
with the cable, never from the VPS:

```sh
cd firmware
WIFI_SSID=... WIFI_PASS=... MQTT_PASS=<printer password> cargo run --release
```

Flash and deploy together when a change touches both: the two ends share
the MQTT message shapes and the timeouts.
