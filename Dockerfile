FROM golang:1.21-alpine3.19 as builder

WORKDIR /build
COPY . .

RUN go build -o migrate ./cmd/migrate/main.go \
    && go build -o watcher ./cmd/watcher/main.go \
    && go build -o web ./cmd/web/main.go

FROM alpine:3.19

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /build/migrate ./migrate
COPY --from=builder /build/watcher ./watcher
COPY --from=builder /build/web ./web
COPY --from=builder /build/templates ./templates
