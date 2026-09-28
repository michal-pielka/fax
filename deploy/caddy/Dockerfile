# Caddy with the rate_limit handler, which stock Caddy does not ship.
# Built by the images workflow like the Go services; the VPS only pulls it.
#
#   docker build -t fax-caddy deploy/caddy
#
# The plugin is pinned to a commit: its only release tag is years behind.
FROM caddy:2-builder AS build
RUN xcaddy build --with github.com/mholt/caddy-ratelimit@5625512f24f6f59d6f64fb3aafe5eecff0b286db

FROM caddy:2-alpine
COPY --from=build /usr/bin/caddy /usr/bin/caddy
