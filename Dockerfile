FROM golang:1.27.3-alpine AS builder

# CGO required for goheif (HEIC/HEIF decoding via libde265)
RUN apk add --no-cache git gcc g++ musl-dev libde265-dev
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./... && go vet ./... && go build -ldflags="-s -w" -o dukecam .

FROM alpine:3.23
# libde265 runtime library required by goheif
RUN apk add --no-cache ca-certificates libde265 curl && addgroup -g 10001 pilot && adduser -D -H -u 10001 -G pilot pilot
WORKDIR /app
COPY --from=builder /build/dukecam .
COPY --from=builder /build/templates ./templates
COPY --from=builder /build/static ./static
COPY --from=builder /build/comparison ./comparison

USER 10001:10001
HEALTHCHECK --interval=30s --timeout=4s --start-period=30s --retries=3 CMD curl --fail --silent --unix-socket /run/dukecam/dukecam.sock http://localhost/api/health -o /dev/null || exit 1
CMD ["./dukecam"]
