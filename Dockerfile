# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/dlsite-rss .

FROM alpine:3.22
# Chromium is needed to pass DLsite's bot protection and read the full top-100 ranking.
RUN apk add --no-cache chromium nss freetype harfbuzz ttf-freefont font-noto-cjk ca-certificates tzdata tini wget \
    && addgroup -S app && adduser -S -G app -h /data app \
    && mkdir -p /data && chown app:app /data
COPY --from=build /out/dlsite-rss /usr/local/bin/dlsite-rss
ENV DATA_DIR=/data PORT=6050 CHROME_PATH=/usr/bin/chromium-browser
USER app
VOLUME /data
EXPOSE 6050
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD wget -qO- http://127.0.0.1:${PORT}/health || exit 1
ENTRYPOINT ["/sbin/tini","--","dlsite-rss"]
