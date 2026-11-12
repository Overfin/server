# The image of Overfin's Jellyfin: Jellyfin's official image, with Overfin's
# plugin for it built in. Each Overfin release comes with this image at the
# same version (ghcr.io/overfin/overfin and ghcr.io/overfin/overfin-jellyfin),
# and Overfin starts only beside the Jellyfin and plugin it's made with.
#
# docker build -t overfin-jellyfin jellyfin
# The plugin is the same on every architecture, so it's built once, on the
# builder's own platform.

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
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.description="Overfin's Jellyfin: Jellyfin (github.com/jellyfin/jellyfin) with Overfin's plugin (github.com/Overfin/jellyfin-plugin)"
COPY --from=plugin /out /usr/lib/overfin/jellyfin-plugin
COPY --chmod=755 entrypoint.sh /usr/lib/overfin/entrypoint.sh
# Jellyfin's data and cache: folders of the user the container runs as
# (65532, not root), volumes of their own.
RUN mkdir -p /config /cache && chown 65532:65532 /config /cache
USER 65532:65532
VOLUME /config /cache
EXPOSE 8096
ENTRYPOINT ["/usr/lib/overfin/entrypoint.sh"]
