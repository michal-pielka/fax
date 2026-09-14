# Build context is the server/ directory:
#   docker build -f cmd/gateway/Dockerfile -t fax-gateway .
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /gateway ./cmd/gateway

# An empty directory to become the photos volume. It has to exist in the image
# owned by the runtime user: Docker seeds a new named volume from the image's
# directory, permissions included, and scratch has nobody to chown it later.
RUN mkdir -p /photos && chown 65534:65534 /photos

# Only ever speaks plain HTTP to the renderer and dispatcher on the compose
# network. TLS to the outside world is Caddy's job, so no certificates here.
FROM scratch
COPY --from=build /gateway /gateway
COPY --from=build --chown=65534:65534 /photos /var/lib/fax/photos

EXPOSE 8080
USER 65534:65534

ENTRYPOINT ["/gateway"]
CMD ["-addr", ":8080"]
