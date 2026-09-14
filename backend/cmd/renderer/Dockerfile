# Build context is the server/ directory:
#   docker build -f cmd/renderer/Dockerfile -t fax-renderer .
# The compiler runs on whatever is building -- an ARM laptop, an x86 runner --
# and emits a binary for the platform being built for. Go cross-compiles
# natively, so an x86 image from an ARM machine takes seconds, not minutes of
# emulating an x86 compiler.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /renderer ./cmd/renderer

# No network calls out, no TLS, no filesystem: nothing for scratch to lack.
FROM scratch
COPY --from=build /renderer /renderer

EXPOSE 8081
USER 65534:65534

ENTRYPOINT ["/renderer"]
CMD ["-addr", ":8081"]
