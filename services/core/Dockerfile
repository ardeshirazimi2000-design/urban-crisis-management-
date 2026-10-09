# Static binaries on an empty (scratch) base: no shell, no package manager, non-root.
# Only Docker Hub images are used so a registry mirror is enough where gcr.io/ghcr.io are unreachable.
FROM golang:1.24-bookworm AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
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
