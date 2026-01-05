# ─── BUILDER STAGE ───────────────────────────────────────────────────────────────
# NOTE: Build from parent directory containing both bor/ and triedb/:
# cd /path/to/polygon && docker build -t bor:kamui-triedb-witness -f bor/Dockerfile .
FROM golang:1.25-alpine AS builder

ARG BOR_DIR=/var/lib/bor/
ENV BOR_DIR=$BOR_DIR
ENV RUSTUP_HOME=/usr/local/rustup
ENV CARGO_HOME=/usr/local/cargo
ENV PATH="/usr/local/cargo/bin:${PATH}"

RUN apk add --no-cache build-base git linux-headers curl

# Install Rust toolchain with cache mount to avoid re-downloading on every build
RUN --mount=type=cache,target=/usr/local/rustup \
    --mount=type=cache,target=/usr/local/cargo \
    if [ ! -f /usr/local/cargo/bin/rustc ]; then \
        curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --no-modify-path; \
    fi

WORKDIR /var/lib/

# Copy both bor and triedb directories (build context should be parent directory)
COPY bor/ bor/
COPY triedb/ triedb/

WORKDIR ${BOR_DIR}

# Initialize git submodules, download Go dependencies, and build (includes triedb-ffi)
RUN --mount=type=ssh \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/rustup \
    --mount=type=cache,target=/usr/local/cargo \
    git submodule update --init --recursive && \
    go mod download && \
    make bor

# ─── RUNTIME STAGE ────────────────────────────────────────────────────────────────
FROM alpine:latest

ARG BOR_DIR=/var/lib/bor/
ENV BOR_DIR=$BOR_DIR

RUN apk add --no-cache bash ca-certificates libgcc && \
    mkdir -p ${BOR_DIR}

WORKDIR ${BOR_DIR}

COPY --from=builder ${BOR_DIR}/build/bin/bor /usr/bin/

EXPOSE 8545 8546 8547 30303 30303/udp

ENTRYPOINT ["bor"]
