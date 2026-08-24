# Build context is the server/ directory:
#   docker build -f cmd/renderer/Dockerfile -t fax-renderer .
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /renderer ./cmd/renderer

# No network calls out, no TLS, no filesystem: nothing for scratch to lack.
FROM scratch
COPY --from=build /renderer /renderer

EXPOSE 8081
USER 65534:65534

ENTRYPOINT ["/renderer"]
CMD ["-addr", ":8081"]
