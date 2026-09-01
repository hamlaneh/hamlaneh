# Hamlaneh server image.
#
# Build context is the REPOSITORY ROOT (docker-compose.yml sets
# `context: ..` / `dockerfile: deploy/Dockerfile`), or manually:
#   docker build -f deploy/Dockerfile .
#
# Three stages: node builds the web application, golang embeds it in the
# binary, distroless static runs it (no shell, no package manager,
# non-root). Only the last stage ships — the final image contains the
# binary and nothing else.

# The MLS wrapper, compiled to WebAssembly (ADR 006). Rust exists in this
# image build and nowhere else in the product: the server stays a single Go
# binary, and this stage's output is a static asset the web bundle lazy-loads
# when someone opens an encrypted conversation. Same toolchain versions as
# webapp/src-mls/Dockerfile (the dev-machine build) — drift between the two
# would ship a bundle nobody tested.
FROM rust:1.91-bookworm@sha256:c1e5f19e773b7878c3f7a805dd00a495e747acbdc76fb2337a4ebf0418896b33 AS mlswasm

RUN rustup target add wasm32-unknown-unknown \
 && cargo install wasm-bindgen-cli --version 0.2.127 --locked

WORKDIR /crate
COPY webapp/src-mls/Cargo.toml webapp/src-mls/Cargo.lock ./
COPY webapp/src-mls/src ./src
# --locked: the committed Cargo.lock is obeyed exactly, as npm ci obeys the
# npm lockfile. The bundler target is the only one the shipped bundle needs;
# pkg-node exists solely for vitest and is not built here.
RUN cargo build --release --target wasm32-unknown-unknown --locked \
 && wasm-bindgen --target bundler --out-dir pkg --out-name hamlaneh_mls \
      target/wasm32-unknown-unknown/release/hamlaneh_mls.wasm \
 && echo '{"type":"module"}' > pkg/package.json

# Node LTS, pinned by digest (CLAUDE.md "CI gates": base images pinned by
# digest; Dependabot's docker ecosystem covers deploy/ and keeps it fresh).
FROM node:24-bookworm-slim@sha256:3638d9a6fe4030bd716be989438248074489337ba3275657f93595428be4fc03 AS webbuild

WORKDIR /src/webapp

# Cache the dependency install separately from source changes. `npm ci`, not
# `npm install`: the lockfile is committed and must be obeyed exactly.
COPY webapp/package.json webapp/package-lock.json ./
RUN npm ci

COPY webapp/ ./
# The compiled MLS wrapper, in the place vite.config.ts's alias resolves.
# Before `npm run build`, because rollup resolves the dynamic import at
# build time — without this the image build fails, which is the correct
# failure: an image without the wasm would 404 the moment somebody opened
# an encrypted conversation.
COPY --from=mlswasm /crate/pkg/ ./src-mls/pkg/
# `npm run build` is `tsc --noEmit && vite build`, so a type error fails the
# image build rather than shipping a broken bundle.
RUN npm run build


FROM golang:1.27@sha256:0ecdc2a9f6156af6451080bfe3d8382a662fcc4e209608c6f919e643453514c1 AS build

WORKDIR /src/server

# Cache module downloads separately from source changes.
# The go.su[m] glob keeps this COPY valid before the first dependency
# (and therefore go.sum) exists.
COPY server/go.mod server/go.su[m] ./
RUN go mod download

COPY server/ ./

# internal/webassets/dist is a committed placeholder that exists only so
# `go build ./...` works without a webapp build (a go:embed of a missing
# directory is a compile error). Replace it wholesale with the real one, so
# no placeholder file can survive into the binary.
RUN rm -rf ./internal/webassets/dist
COPY --from=webbuild /src/webapp/dist/ ./internal/webassets/dist/

# The release tag, stamped into the binary so `hamlaneh-server --version`
# answers. deploy/hamlaneh-update.sh asks the RUNNING server what it is
# (`compose exec server hamlaneh-server --version`) and fails the update when
# the answer is not a version, because the installed version is the only input
# to verify-release.sh's anti-rollback check. The release workflow passes
# --build-arg VERSION; a local `docker compose build` gets "dev", which is
# honest and which the updater refuses rather than guesses at.
ARG VERSION=dev

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/hamlaneh-server ./cmd/hamlaneh-server

# The data directory, created here because the runtime stage is distroless
# and has no shell to mkdir with. Docker seeds a fresh named volume from
# whatever the image has at the mount point, ownership included, and that is
# the only thing that lets the non-root server write to it — a volume Docker
# creates from nothing is owned by root, and the container cannot chown it.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /out/hamlaneh-server /usr/local/bin/hamlaneh-server
COPY --from=build --chown=65532:65532 /out/data /var/lib/hamlaneh

# Numeric UID of the distroless "nonroot" user, stated explicitly so
# runtime non-root checks are unambiguous.
USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/hamlaneh-server"]
