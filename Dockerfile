# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS build
ARG VERSION=development
WORKDIR /src/companion
COPY companion/go.mod companion/go.sum ./
RUN go mod download
COPY companion/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -buildvcs=false -trimpath -ldflags='-s -w' -o /out/ai-emby-worker .
WORKDIR /src/gateway
COPY gateway/go.mod gateway/go.sum ./
RUN go mod download
COPY gateway/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -buildvcs=false -trimpath -ldflags="-s -w -X main.releaseVersion=$VERSION" -o /out/ai-emby-gateway .
RUN printf '%s\n' "$VERSION" > /out/VERSION

FROM postgres:17-bookworm
USER root
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget ffmpeg \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /app/data /app/backups /app/update-control /media /run/secrets
COPY runtime/ai-emby-core-linux-amd64 /usr/local/bin/ai-emby-core
COPY --from=build /out/ai-emby-worker /out/ai-emby-gateway /usr/local/bin/
COPY --from=build /out/VERSION /app/VERSION
COPY frontend/ /app/frontend/
RUN chmod 755 /usr/local/bin/ai-emby-core /usr/local/bin/ai-emby-worker /usr/local/bin/ai-emby-gateway
LABEL org.opencontainers.image.title="ai-emby" \
      org.opencontainers.image.source="https://github.com/LLL198/ai-emby"
WORKDIR /app
ENV LISTEN=:8097 MEDIA_INFO_ROOT=/app/data FILE_MANAGER_ROOT=/media
EXPOSE 8097
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=5 CMD wget -q -O /dev/null http://127.0.0.1:8097/health || exit 1
ENTRYPOINT ["/usr/local/bin/ai-emby-gateway"]
