# syntax=docker/dockerfile:1

# ---- web interface ----
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- organisation (ASN) database, baked in so the app works offline ----
# IP to ASN Lite by DB-IP (https://db-ip.com), CC BY 4.0. A new file is
# published each month; the previous month is used if the current one is not
# out yet.
FROM --platform=$BUILDPLATFORM alpine:3.21 AS asn
RUN apk add --no-cache curl
RUN set -eu; \
    y="$(date -u +%Y)"; m="$(date -u +%m)"; m="${m#0}"; \
    if [ "$m" -eq 1 ]; then py=$((y - 1)); pm=12; else py="$y"; pm=$((m - 1)); fi; \
    for month in "$(printf '%04d-%02d' "$y" "$m")" "$(printf '%04d-%02d' "$py" "$pm")"; do \
      if curl -fsSL "https://download.db-ip.com/free/dbip-asn-lite-${month}.mmdb.gz" -o /asn.mmdb.gz; then break; fi; \
    done; \
    gunzip /asn.mmdb.gz; test -s /asn.mmdb

# ---- server ----
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go ./web/embed.go
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/mikrotik-home-netflow-plus ./cmd/mikrotik-home-netflow-plus
# An empty /data owned by the runtime user, so a fresh named volume is writable.
RUN mkdir -p /out/data

# ---- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="mikrotik-home-netflow-plus" \
      org.opencontainers.image.description="Live network traffic per connection and per device for MikroTik routers (IPFIX + RouterOS API), with a web interface and wall display." \
      org.opencontainers.image.source="https://github.com/thedyerman/mikrotik-home-netflow-plus" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY LICENSE NOTICE /licenses/
COPY --from=build /out/mikrotik-home-netflow-plus /mikrotik-home-netflow-plus
COPY --from=build --chown=nonroot:nonroot /out/data /data
COPY --from=asn /asn.mmdb /usr/share/nfp/asn.mmdb
ENV NFP_DATA_DIR=/data
VOLUME /data
EXPOSE 8080/tcp 2055/udp
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD ["/mikrotik-home-netflow-plus", "healthcheck"]
ENTRYPOINT ["/mikrotik-home-netflow-plus"]
