FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/whatsgram ./cmd/whatsgram

FROM alpine:3.21

RUN addgroup -S whatsgram && adduser -S -G whatsgram whatsgram

COPY --from=build /out/whatsgram /usr/local/bin/whatsgram

USER whatsgram
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/whatsgram"]
