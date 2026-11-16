# Overfin's gateway: the only part the internet reaches (see
# cmd/overfin-gateway). A static binary alone in a minimal image.
#
# docker build -t overfin-gateway .
# The build runs on the builder's own platform and cross-compiles for the
# target, so images for other architectures build without emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.0@sha256:4013ae0f9e7994f8535c58c811f8f863fbed38b72e0d51e6592156f758d66146 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.Version=${VERSION}" -o /overfin-gateway ./cmd/overfin-gateway
# The final image has no shell, so its folders are prepared here: its data,
# and the one-machine setup's pairing volume.
RUN mkdir /data /pairing

# distroless/static: no shell, no package manager, runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.source="https://github.com/Overfin/server" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.description="Overfin's gateway: the only part of Overfin the internet reaches"
COPY --from=build /overfin-gateway /overfin-gateway
# 65532 is the distroless "nonroot" user the container runs as. Its folder:
# its own key, its backend's, its certificates (nothing personal).
COPY --from=build --chown=65532:65532 /data /data
COPY --from=build --chown=65532:65532 /pairing /pairing
VOLUME /data
# 8080: the site (plain http, or Let's Encrypt's checks and a redirect with
# OVERFIN_DOMAIN); 8443: the site over HTTPS, the backend's link sharing
# it; 8444: the link's own listener, without a domain.
EXPOSE 8080 8443 8444
ENTRYPOINT ["/overfin-gateway"]
