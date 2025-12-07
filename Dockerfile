FROM golang:1.25.5-alpine AS builder

RUN apk add --no-cache git

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o migrate ./cmd/migrate/main.go \
    && go build -o watcher ./cmd/watcher/main.go \
    && go build -o web ./cmd/web/main.go

FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /build/migrate ./migrate
COPY --from=builder /build/watcher ./watcher
COPY --from=builder /build/web ./web
COPY --from=builder /build/templates ./templates
