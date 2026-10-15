FROM golang:1.26-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /build/whatsgram ./cmd/whatsgram

FROM alpine:3.21

RUN apk add --no-cache \
    ca-certificates \
    ffmpeg \
    tzdata

RUN addgroup -g 1000 appgroup && \
    adduser -u 1000 -G appgroup -s /bin/sh -D appuser

RUN mkdir -p /data && chown -R appuser:appgroup /data

VOLUME ["/data"]

USER appuser

WORKDIR /data

COPY --from=builder /build/whatsgram /usr/local/bin/whatsgram

ENTRYPOINT ["/usr/local/bin/whatsgram"]
CMD []
