# Cross-compiles on the build machine for each target platform, so
# multi-arch builds need no emulation. SQLite is pure Go: the binary is static.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/scrobbler ./cmd/scrobbler && \
    mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="tay-backend-scrobbler" \
      org.opencontainers.image.description="Scrobbles YouTube Music listening history to Last.fm" \
      org.opencontainers.image.source="https://github.com/alator21/tay-backend-scrobbler" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/scrobbler /usr/local/bin/scrobbler
# Database, snapshot, alert state and (by default) cookie.txt live here.
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV DATA_DIR=/data
VOLUME /data
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/scrobbler"]
CMD ["run"]
