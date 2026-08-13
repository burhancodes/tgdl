# Pinned to specific LTS version for build reproducibility. Update deliberately when upgrading base image.
FROM ubuntu:24.04

# Avoid prompt dialogs during package installation
ENV DEBIAN_FRONTEND=noninteractive

# Install media tools, torrent client, archive extraction libraries, and curl/ca-certificates
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 \
    ca-certificates \
    git \
    ffmpeg \
    aria2 \
    unzip \
    unrar \
    p7zip-full \
    tar \
    gzip \
    bzip2 \
    xz-utils \
    && rm -rf /var/lib/apt/lists/*

# Install uv from official image
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/

WORKDIR /app

# Copy dependency specifications for layer caching
COPY pyproject.toml uv.lock .python-version ./

# Install dependencies using uv
RUN uv sync --frozen --no-cache --no-dev

# Copy application source code
COPY app ./app

RUN useradd -m botuser && mkdir -p /app/data /app/logs && chown -R botuser:botuser /app
USER botuser

VOLUME ["/app/data", "/app/logs"]

ENTRYPOINT ["uv", "run", "python", "-m", "app.bot"]
