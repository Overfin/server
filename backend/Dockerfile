# The image of Overfin's backend: Overfin's backend program, with Jellyfin's
# official image and Overfin's plugin for it built in. The backend puts the
# plugin in place, keeps Jellyfin to the container and runs it. Each Overfin
# release comes with this image and the gateway's at the same version.
#
# docker build -f backend/Dockerfile -t overfin-backend .
# The Go build runs on the builder's own platform and cross-compiles for the
# target; the plugin is the same on every architecture, so it's built once,
# on the builder's own platform too.
#
# Branch and test builds, before the sources they need are released, give
# their local checkouts as named build contexts; a release build gives
# none and uses go.mod's versions and the plugin's pinned commit:
#
# docker buildx build -f backend/Dockerfile -t overfin-backend:test \
#   --build-context msfp-go-source=../msfp-go \
#   --build-context overfin-web-source=../overfin-web \
#   --build-context plugin-source=../overfin-jellyfin-plugin \
#   --build-arg PLUGIN_VERSION=0.9.0 .

# Empty unless given as build contexts (above).
FROM scratch AS msfp-go-source
FROM scratch AS overfin-web-source
FROM scratch AS plugin-source

FROM --platform=$BUILDPLATFORM golang:1.27.0@sha256:4013ae0f9e7994f8535c58c811f8f863fbed38b72e0d51e6592156f758d66146 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
# A local source given replaces its module (go.mod's own replace lines,
# for builds outside a container, name paths of the builder's machine).
COPY --from=msfp-go-source / /deps/msfp-go/
COPY --from=overfin-web-source /go.mo[d] /go.su[m] /web.g[o] /deps/overfin-web/
COPY --from=overfin-web-source /dis[t] /deps/overfin-web/dist/
COPY --from=overfin-web-source /src/lib/sd[k] /deps/overfin-web/src/lib/sdk/
RUN for m in msfp-io/msfp-go=msfp-go overfin/web=overfin-web; do \
      if [ -f "/deps/${m#*=}/go.mod" ]; then go mod edit -replace "github.com/${m%=*}=/deps/${m#*=}"; fi; \
    done && mkdir -p /deps && cp go.mod /deps/go.mod && go mod download
COPY . .
RUN cp /deps/go.mod go.mod
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/overfin/server/overfin.Version=${VERSION}" -o /overfin-backend ./cmd/overfin-backend

# Overfin's plugin for Jellyfin (github.com/Overfin/jellyfin-plugin, GPL),
# at the commit this Overfin was made with (or a local checkout given as
# plugin-source, for branch and test builds), built against the Jellyfin
# below.
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/dotnet/sdk:10.0.404@sha256:fe0fd4e93bd2fbb47c883b7a6369e5c8d3db8bf01cdc5981f6ee35fbdf3ff4fb AS plugin
ARG PLUGIN_COMMIT=72c2d7a6cf8a93132c6fcc16e607fe8b901920a3
ARG PLUGIN_VERSION=0.8.0
COPY --from=plugin-source /sr[c] /local/src/
RUN if [ -d /local/src/Overfin.Jellyfin ]; then \
      mkdir /plugin && cp -r /local/src /plugin/src && rm -rf /plugin/src/*/bin /plugin/src/*/obj && cd /plugin; \
    else \
      git clone --quiet https://github.com/Overfin/jellyfin-plugin /plugin && cd /plugin && git checkout --quiet "$PLUGIN_COMMIT" \
      && test "$(git rev-parse HEAD)" = "$PLUGIN_COMMIT"; \
    fi \
 && dotnet build -c Release src/Overfin.Jellyfin -o /out/$PLUGIN_VERSION \
 && find /out/$PLUGIN_VERSION -type f ! -name Overfin.Jellyfin.dll -delete

# Jellyfin's official image (GPL; its source: github.com/jellyfin/jellyfin),
# the version Overfin is made for, with its ffmpeg and drivers' libraries.
FROM docker.io/jellyfin/jellyfin:12.2.20261005-225228@sha256:357724bf0ae27a672c7cbaa899db2d9abeb13dbd8657ccce750258a4c059d037
LABEL org.opencontainers.image.source="https://github.com/Overfin/server" \
      org.opencontainers.image.licenses="MIT AND GPL-3.0-only" \
      org.opencontainers.image.description="Overfin's backend, with Jellyfin (github.com/jellyfin/jellyfin) and Overfin's plugin for it (github.com/Overfin/jellyfin-plugin)"
COPY --from=build /overfin-backend /overfin-backend
COPY --from=plugin /out /usr/lib/overfin/jellyfin-plugin
# Overfin's data and its secret key (apart, so a copy of the data never
# carries its key), and Jellyfin's data and cache: folders of the user the
# container runs as (65532, not root), volumes of their own.
RUN mkdir -p /data /secrets /config /cache /local /pairing && chown 65532:65532 /data /secrets /config /cache /local /pairing
USER 65532:65532
VOLUME /data /secrets /config /cache
# 8081: the control panel and the setup wizard, for private addresses only;
# 8082: Jellyfin's own pages, only while access to it is open in the
# panel, for private addresses only.
# Nothing else listens outside the container: the site goes to the gateway
# over the link the backend opens, and Jellyfin listens inside only.
EXPOSE 8081 8082
ENTRYPOINT ["/overfin-backend"]
