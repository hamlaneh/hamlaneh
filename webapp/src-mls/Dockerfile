# The toolchain that builds the MLS wrapper to wasm.
#
# Rust is not a development-host requirement for this project and is not going
# to become one: this image is the whole answer, and `./build.sh` drives it.
FROM rust:1.91-bookworm

RUN rustup target add wasm32-unknown-unknown
RUN cargo install wasm-bindgen-cli --version 0.2.127 --locked
# cargo-audit rides in the same image so the RustSec gate (ADR 006 — the
# critical hpke-rs bug lived one layer below the audited library) needs no
# second toolchain anywhere: CI and a dev machine run it the same way.
RUN cargo install cargo-audit --locked

WORKDIR /crate
