# Overfin's image: Overfin, its Jellyfin and Overfin's plugin for it, at
# versions made for each other. It starts as one of three roles
# (OVERFIN_ROLE): gateway (Overfin alone, the default), all (Overfin and its
# Jellyfin together) or jellyfin (Jellyfin and the plugin alone).
#
# docker build -t overfin .
# The Go build runs on the builder's own platform and cross-compiles for the
# target, so images for other architectures build without emulation; the
# plugin is the same on every architecture.

FROM --platform=$BUILDPLATFORM golang:1.26.11@sha256:957952ae74dc010608717144399a59a2c6e82ea03d63be1c7ff6577c5a135f93 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/overfin/server/overfin.Version=${VERSION}" -o /overfin ./cmd/overfin

# Overfin's plugin for Jellyfin (github.com/Overfin/jellyfin-plugin, GPL),
# at the commit this Overfin was made with, built against the Jellyfin
# below.
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/dotnet/sdk:10.0.404@sha256:fe0fd4e93bd2fbb47c883b7a6369e5c8d3db8bf01cdc5981f6ee35fbdf3ff4fb AS plugin
ARG PLUGIN_COMMIT=2a45af0f6b4322c74bda8f6279863aeb685412fb
ARG PLUGIN_VERSION=0.1.0
RUN git clone --quiet https://github.com/Overfin/jellyfin-plugin /plugin && cd /plugin && git checkout --quiet "$PLUGIN_COMMIT" \
 && test "$(git rev-parse HEAD)" = "$PLUGIN_COMMIT" \
 && dotnet build -c Release src/Overfin.Jellyfin -o /out/$PLUGIN_VERSION \
 && find /out/$PLUGIN_VERSION -type f ! -name Overfin.Jellyfin.dll -delete

# Jellyfin's official image (GPL; its source: github.com/jellyfin/jellyfin),
# the version Overfin is made for, with its ffmpeg and drivers' libraries.
FROM docker.io/jellyfin/jellyfin:12.2.20261005-225228@sha256:357724bf0ae27a672c7cbaa899db2d9abeb13dbd8657ccce750258a4c059d037
LABEL org.opencontainers.image.source="https://github.com/Overfin/server" \
      org.opencontainers.image.licenses="MIT AND GPL-3.0-only" \
      org.opencontainers.image.description="Overfin, with its own Jellyfin (github.com/jellyfin/jellyfin) and Overfin's plugin for it (github.com/Overfin/jellyfin-plugin)"
COPY --from=build /overfin /overfin
COPY --from=plugin /out /usr/lib/overfin/jellyfin-plugin
# Overfin's data and secret key, and Jellyfin's data and cache: folders of
# the user the container runs as (65532, not root), volumes of their own.
RUN mkdir -p /data /secrets /config /cache && chown 65532:65532 /data /secrets /config /cache
USER 65532:65532
# Jellyfin's own health check asks Jellyfin, which the gateway role doesn't run.
HEALTHCHECK NONE
VOLUME /data /secrets /config /cache
# 8080: the site (plain http, or Let's Encrypt's checks with https.domain);
# 8443: the site over HTTPS, with https.domain; 8081: the control panel's own
# listener, with an identity provider; 8096: Jellyfin, in the jellyfin role
# only (in the all role it listens inside the container alone).
EXPOSE 8080 8443 8081 8096
ENTRYPOINT ["/overfin"]
