# syntax=docker/dockerfile:1
#
# Vynull: a headless virtual CDJ / rekordbox source for Pro DJ Link.
#
# IMPORTANT: Pro DJ Link relies on UDP broadcast discovery and binding a real
# network interface, so the container must run with host networking. Bridged
# networking will not see the CDJs. See the "docker run" example below.

# ---- build: a static Go binary (the project is pure Go, no cgo) ----
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vynull .

# ---- runtime ----
FROM debian:bookworm-slim

# ffmpeg: audio decode/transcode, artwork, and waveform thumbnails.
# python3 + venv: master.db import runs tools/rekordbox_dump.py against a
# pre-provisioned sqlcipher3 venv, so there is no pip or network at runtime.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ffmpeg python3 python3-venv ca-certificates \
 && python3 -m venv /opt/venv \
 && /opt/venv/bin/pip install --no-cache-dir sqlcipher3-binary \
 && rm -rf /var/lib/apt/lists/* /root/.cache

COPY --from=build /out/vynull /usr/local/bin/vynull
COPY tools/rekordbox_dump.py /app/tools/rekordbox_dump.py

# VYNULL_PYTHON points master.db import at the pre-built venv (skips the runtime
# venv/pip entirely); VYNULL_DUMP_SCRIPT locates the helper. State (analysis
# cache, settings, artwork) lives under $HOME, mounted as the /data volume.
ENV VYNULL_PYTHON=/opt/venv/bin/python3 \
    VYNULL_DUMP_SCRIPT=/app/tools/rekordbox_dump.py \
    HOME=/data
VOLUME ["/data"]

# Cosmetic under host networking (which Pro DJ Link requires); a hint otherwise.
EXPOSE 9443

ENTRYPOINT ["vynull"]
# Example (compose into one line at the shell):
#   docker run --rm --network host \
#     -v vynull-state:/data -v /path/to/music:/music:ro \
#     ghcr.io/vynulldev/vynull --interface eth0 --music-dir /music \
#     --web --listen 0.0.0.0:9443
