# Static binaries on an empty (scratch) base: no shell, no package manager, non-root.
# Go dependencies are vendored (services/core/vendor), so the build needs no network access;
# only Docker Hub base images are used, so a registry mirror is enough where gcr.io is unreachable.
FROM golang:1.24-bookworm AS build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=vendor GOPROXY=off
WORKDIR /src
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/... \
 && mkdir -p /rootfs/data/objects /rootfs/tmp && chown -R 65532:65532 /rootfs/data /rootfs/tmp

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build --chown=65532:65532 /rootfs/ /
COPY --from=build /out/ /app/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/api"]
