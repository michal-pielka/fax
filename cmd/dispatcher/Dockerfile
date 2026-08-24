# Build context is the server/ directory:
#   docker build -f cmd/dispatcher/Dockerfile -t fax-dispatcher .
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /dispatcher ./cmd/dispatcher

FROM alpine:3.21
# Needed the moment the broker URL becomes ssl://. Cheap to include now rather
# than debugging an x509 error during the VPS move.
RUN apk add --no-cache ca-certificates

COPY --from=build /dispatcher /dispatcher

EXPOSE 8082
USER 65534:65534

ENTRYPOINT ["/dispatcher"]
CMD ["-addr", ":8082"]
