# Hamlaneh server image.
#
# Build context is the REPOSITORY ROOT (docker-compose.yml sets
# `context: ..` / `dockerfile: deploy/Dockerfile`), or manually:
#   docker build -f deploy/Dockerfile .
#
# Multi-stage: golang builder -> distroless static (no shell, no package
# manager, non-root). The final image contains the binary only.

FROM golang:1.26 AS build

WORKDIR /src/server

# Cache module downloads separately from source changes.
# The go.su[m] glob keeps this COPY valid before the first dependency
# (and therefore go.sum) exists.
COPY server/go.mod server/go.su[m] ./
RUN go mod download

COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
      -o /out/hamlaneh-server ./cmd/hamlaneh-server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/hamlaneh-server /usr/local/bin/hamlaneh-server

# Numeric UID of the distroless "nonroot" user, stated explicitly so
# runtime non-root checks are unambiguous.
USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/hamlaneh-server"]
