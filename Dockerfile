ARG BUILD_STAGE=builder
ARG PREBUILT_PATH=dist/whatsgram

FROM golang:1.26-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /build/whatsgram ./cmd/whatsgram

FROM scratch AS prebuilt

ARG PREBUILT_PATH=dist/whatsgram
COPY ${PREBUILT_PATH} /build/whatsgram

FROM ${BUILD_STAGE} AS binary-source

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

COPY --from=binary-source /build/whatsgram /usr/local/bin/whatsgram

ENTRYPOINT ["/usr/local/bin/whatsgram"]
CMD []
