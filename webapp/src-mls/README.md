# `src-mls` — the MLS wrapper

The webapp's only MLS-literate component, and the only Rust in the client
build. It compiles to WebAssembly and is loaded lazily by
`webapp/src/mls/wasm.ts`, which is the only file that imports it.

Named after Tauri's `src-tauri` convention: a sibling crate to the TypeScript
app rather than something buried inside `src/`.

## What it is, and what it is not

It is glue. Every cryptographic operation is a call into
[OpenMLS](https://openmls.tech) 0.9.0 on the `openmls_rust_crypto` provider,
at ciphersuite `MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519`. There is no
hand-rolled cryptography here and there never will be (PLAN.md §6.9); a change
that needs reasoning about a protocol rule rather than about plumbing belongs
upstream.

The one substantial thing it implements itself is
`openmls_traits::storage::StorageProvider` (`src/storage.rs`) over an in-memory
map it owns, with wholesale export and import across the wasm boundary. The
alternative — `openmls_rust_crypto::MemoryStorage` plus its public `values`
field — is what the integration spike used and is nobody's stability promise
(`docs/spikes/mls-wasm-integration.md` §4).

**The exported map holds raw MLS secrets in the clear**: the signature private
key, epoch secrets, message secrets. That is a property of the storage trait,
not a choice made here. `webapp/src/mls/keystore.ts` encrypts every export
before it reaches IndexedDB and documents honestly what that does and does not
resist.

## Building

This development host has no Rust toolchain, deliberately, so the build runs
in a container:

```bash
webapp/src-mls/build.sh
```

The first run builds the toolchain image (`rust:1.91-bookworm` plus the
`wasm32-unknown-unknown` target and `wasm-bindgen-cli` 0.2.127) and takes
several minutes; later runs reuse it and a cached cargo registry. Output:

| Directory  | wasm-bindgen target | Used by |
|---|---|---|
| `pkg/`      | `bundler` | Vite, i.e. the shipped application |
| `pkg-node/` | `nodejs`  | the vitest round-trip tests, which cannot resolve the bundler target's wasm import |

Neither is committed. Measured at the pinned versions: **1,548,850 bytes raw,
480,505 gzipped** for the wasm, plus 5,441 gzipped for the JS glue. The
release profile in `Cargo.toml` (`opt-level = "z"`, LTO, one codegen unit,
`panic = "abort"`, stripped) is the measured-cheapest configuration for
gzipped bytes. **Do not add a `wasm-opt` pass**: it shrinks the file raw and
grows it gzipped (spike §2).

## Tests

`cargo test` covers the framing and the storage map. It needs no wasm — run it
in the same image:

```bash
docker run --rm -v "$PWD:/crate" -w /crate hamlaneh-mls-build cargo test
```

The end-to-end MLS behaviour (two devices, group creation, Welcome, message
round-trip, restart from an export) is exercised from the TypeScript side in
`webapp/src/mls/wasm.roundtrip.test.ts`. **Those tests skip loudly when
`pkg-node/` is absent** — a local run without a build prints a banner saying
so rather than passing quietly. CI always builds the crate first, so a skip
there would be a broken workflow, not a missing artifact.
