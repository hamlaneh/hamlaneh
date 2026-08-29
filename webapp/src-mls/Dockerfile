# The toolchain that builds the MLS wrapper to wasm.
#
# Rust is not a development-host requirement for this project and is not going
# to become one: this image is the whole answer, and `./build.sh` drives it.
FROM rust:1.91-bookworm

RUN rustup target add wasm32-unknown-unknown
RUN cargo install wasm-bindgen-cli --version 0.2.127 --locked

WORKDIR /crate
