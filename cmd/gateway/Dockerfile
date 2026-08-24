# Build context is the server/ directory:
#   docker build -f cmd/gateway/Dockerfile -t fax-gateway .
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /gateway ./cmd/gateway

# Only ever speaks plain HTTP to the renderer and dispatcher on the compose
# network. TLS to the outside world is Caddy's job, so no certificates here.
FROM scratch
COPY --from=build /gateway /gateway

EXPOSE 8080
USER 65534:65534

ENTRYPOINT ["/gateway"]
CMD ["-addr", ":8080"]
