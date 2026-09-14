# Build context is the server/ directory:
#   docker build -f cmd/dispatcher/Dockerfile -t fax-dispatcher .
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
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /dispatcher ./cmd/dispatcher

FROM alpine:3.21
# Needed the moment the broker URL becomes ssl://. Cheap to include now rather
# than debugging an x509 error during the VPS move.
RUN apk add --no-cache ca-certificates

COPY --from=build /dispatcher /dispatcher

EXPOSE 8082
USER 65534:65534

ENTRYPOINT ["/dispatcher"]
CMD ["-addr", ":8082"]
