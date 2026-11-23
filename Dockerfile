# Overfin's gateway: the only part the internet reaches (see
# cmd/overfin-gateway). A static binary alone in a minimal image.
#
# docker build -t overfin-gateway .
# The build runs on the builder's own platform and cross-compiles for the
# target, so images for other architectures build without emulation.
# Branch and test builds give unreleased modules' local checkouts as named
# build contexts, as backend/Dockerfile says (msfp-go-source,
# overfin-web-source); a release build gives none.

FROM scratch AS msfp-go-source
FROM scratch AS overfin-web-source

FROM --platform=$BUILDPLATFORM golang:1.27.0@sha256:4013ae0f9e7994f8535c58c811f8f863fbed38b72e0d51e6592156f758d66146 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
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
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.Version=${VERSION}" -o /overfin-gateway ./cmd/overfin-gateway
# The final image has no shell, so its folders are prepared here: its data,
# the volume it shares with its backend beside it, and the pairing volume
# of setups made before it (two machines pair by hand).
RUN mkdir /data /local /pairing

# distroless/static: no shell, no package manager, runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.source="https://github.com/Overfin/server" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.description="Overfin's gateway: the only part of Overfin the internet reaches"
COPY --from=build /overfin-gateway /overfin-gateway
# 65532 is the distroless "nonroot" user the container runs as. Its folder:
# its own key, its backend's, its certificates (nothing personal).
COPY --from=build --chown=65532:65532 /data /data
COPY --from=build --chown=65532:65532 /local /local
COPY --from=build --chown=65532:65532 /pairing /pairing
VOLUME /data
# 8080: the site (plain http, or Let's Encrypt's checks and a redirect with
# OVERFIN_DOMAIN); 8443: the site over HTTPS, a remote backend's link
# sharing it; 8444: the link's own listener, without a domain; 8090: beside
# its backend and online, the LAN's own site.
EXPOSE 8080 8443 8444 8090
ENTRYPOINT ["/overfin-gateway"]
