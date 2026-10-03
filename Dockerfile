# syntax=docker/dockerfile:1

# ---- build ----------------------------------------------------------------
FROM golang:1.26-bookworm AS build
WORKDIR /src
ENV GOTOOLCHAIN=auto
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tgdl ./cmd/tgdl

# ---- runtime ----------------------------------------------------------------
FROM python:3.12-slim-bookworm AS runtime
ENV DEBIAN_FRONTEND=noninteractive PIP_NO_CACHE_DIR=1 PIP_BREAK_SYSTEM_PACKAGES=1
RUN ( [ -f /etc/apt/sources.list.d/debian.sources ] && sed -i 's/Components: main/Components: main non-free non-free-firmware/' /etc/apt/sources.list.d/debian.sources || true ) \
    && apt-get update && apt-get install -y --no-install-recommends \
       ca-certificates ffmpeg aria2 unzip unrar p7zip-full tar gzip bzip2 xz-utils nodejs npm default-jre-headless \
    && rm -rf /var/lib/apt/lists/*
# External engines that have no Go equivalent are invoked as subprocesses.
RUN pip install gallery-dl cyberdrop-dl-patched

RUN useradd -m -u 1000 botuser
WORKDIR /app
COPY --from=build /out/tgdl /usr/local/bin/tgdl
COPY scraper ./scraper
COPY configs ./configs
RUN cd scraper && npm ci --omit=dev && mkdir -p /app/data /app/auth /app/logs && chown -R botuser:botuser /app
USER botuser
VOLUME ["/app/data", "/app/auth", "/app/logs"]
ENTRYPOINT ["tgdl"]
