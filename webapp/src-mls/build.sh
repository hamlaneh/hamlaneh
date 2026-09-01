#!/usr/bin/env bash
# Builds the MLS wrapper to wasm, in Docker, into pkg/ and pkg-node/.
#
#   webapp/src-mls/build.sh          # build both targets and report sizes
#
# pkg/      wasm-bindgen --target bundler — what Vite ships to the browser.
# pkg-node/ wasm-bindgen --target nodejs  — what the round-trip tests load,
#           because vitest cannot resolve the bundler target's wasm import.
#
# Neither directory is committed (see .gitignore): build output is not source.
set -euo pipefail

# Git Bash rewrites anything that looks like a POSIX path in a docker
# argument, which turns /crate into C:/Program Files/Git/crate. The variable
# stops that, and `pwd -W` hands docker a native path; both are no-ops on
# Linux and macOS, where `pwd -W` simply does not exist.
export MSYS_NO_PATHCONV=1
cd "$(dirname "${BASH_SOURCE[0]}")"
crate_dir="$(pwd -W 2>/dev/null || pwd)"
image="hamlaneh-mls-build"
target="wasm32-unknown-unknown"

if ! docker image inspect "$image" >/dev/null 2>&1; then
  echo "==> building the toolchain image (first run only, several minutes)"
  docker build -t "$image" "$crate_dir"
fi

echo "==> compiling for $target"
# A named volume for the registry and target dir: without it every run
# recompiles ~200 crates from scratch.
docker run --rm \
  -v "$crate_dir:/crate" \
  -v hamlaneh-mls-cargo:/usr/local/cargo/registry \
  -v hamlaneh-mls-target:/crate/target \
  -w /crate \
  "$image" \
  bash -euc '
    cargo build --release --target '"$target"'
    wasm="target/'"$target"'/release/hamlaneh_mls.wasm"
    rm -rf pkg pkg-node
    wasm-bindgen --target bundler --out-dir pkg      --out-name hamlaneh_mls "$wasm"
    wasm-bindgen --target nodejs  --out-dir pkg-node --out-name hamlaneh_mls "$wasm"
    # webapp/package.json says "type": "module", which would otherwise make
    # node read the nodejs target'"'"'s CommonJS glue as ESM. Raw wasm-bindgen
    # (unlike wasm-pack) writes no package.json of its own, so both go here.
    echo "{\"type\":\"commonjs\"}" > pkg-node/package.json
    echo "{\"type\":\"module\"}"   > pkg/package.json
    chmod -R a+rw pkg pkg-node
  '

echo "==> sizes"
for file in "$crate_dir"/pkg/hamlaneh_mls_bg.wasm "$crate_dir"/pkg/hamlaneh_mls_bg.js; do
  raw=$(wc -c <"$file")
  gz=$(gzip -9 -c "$file" | wc -c)
  printf '%-34s raw %9s   gzip -9 %9s\n' "$(basename "$file")" "$raw" "$gz"
done

echo "==> done: $crate_dir/pkg and $crate_dir/pkg-node"
