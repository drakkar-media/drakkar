FROM node:22-alpine AS frontend
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party/ ./third_party/
RUN go mod download
COPY . .
COPY --from=frontend /web/build ./internal/frontend/build
# rapidyenc (SIMD/CGO yEnc decoder) ships prebuilt static libs per-platform
# but still needs a C++ toolchain to link the small cgo glue against them --
# alpine's golang image has no compiler at all, and even with one added,
# linking a glibc-built static archive against musl is a real ABI risk.
# Building on Debian (glibc) sidesteps that entirely. Without this tag the
# build silently falls back to yenc's pure-Go decoder (see
# internal/yenc/decoder_purego.go's `!rapidyenc || !cgo` constraint), which
# is what production had been running the whole time -- confirmed as the
# root cause of a CPU-bound (341% CPU, one process) streaming bottleneck
# that plain read-ahead parallelism tuning couldn't fix.
ENV CGO_ENABLED=1
RUN go build -tags rapidyenc -o /out/drakkar ./cmd/drakkar

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates fuse3 par2 p7zip-full tzdata libstdc++6 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/drakkar /app/drakkar
COPY --from=build /src/migrations /app/migrations
EXPOSE 8080
ENTRYPOINT ["/app/drakkar"]
