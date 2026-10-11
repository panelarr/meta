# syntax=docker/dockerfile:1
#
# Image for the meta service: `make docker` or
#   docker build -t panelarr/meta .

ARG GO_VERSION=1.27.2

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY gen ./gen
COPY internal ./internal
COPY sdk ./sdk
RUN mkdir -p /out/data
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X github.com/panelarr/meta/internal/buildinfo.Version=${VERSION}" \
      -o /out/meta ./cmd/meta

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="meta" \
      org.opencontainers.image.description="Panelarr metadata service: browse, search and details over a merged metadata catalogue" \
      org.opencontainers.image.source="https://github.com/panelarr/meta" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/meta /meta
COPY --from=build --chown=65532:65532 /out/data /data
ENV META_LISTEN=:8901 \
    META_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8901
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/meta", "healthcheck"]
ENTRYPOINT ["/meta"]
CMD ["serve"]
