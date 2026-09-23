# Shopkeet API — production multi-stage build.
# Phase 0: docker compose up pe backend hi build+run hota hai (frontend nahi).
# K8s/Gateway/load-balancer sab "explicitly not building" list mein hain; ye image
# Coolify/VPS (Traefik) ke peeche single replica ke liye bana hai.

FROM golang:1.27-alpine AS build

WORKDIR /src

RUN apk add --no-cache ca-certificates \
    && update-ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO_ENABLED=0: static binary — deletion-able on scratch.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/shopkeet-api ./cmd/api \
    && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/shopkeet-migrate ./cmd/migrate

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S shopkeet && adduser -S -G shopkeet shopkeet

COPY --from=build /out/shopkeet-api /usr/local/bin/shopkeet-api
COPY --from=build /out/shopkeet-migrate /usr/local/bin/shopkeet-migrate

# migrations are embedded via go:embed — no COPY needed at runtime.

USER shopkeet
EXPOSE 3001

CMD ["shopkeet-api"]
