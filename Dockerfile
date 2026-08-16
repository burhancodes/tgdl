# ==========================================
# 1. Builder Stage
# ==========================================
FROM python:3.12-slim AS builder

ENV DEBIAN_FRONTEND=noninteractive

# Install build dependencies and git for fetching git-based packages
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Install uv from official image
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/

WORKDIR /app

# Copy dependency locks
COPY pyproject.toml uv.lock .python-version ./

# Install dependencies into a standalone virtualenv with pre-compiled bytecode
RUN uv sync --frozen --no-dev --no-cache --compile-bytecode

# ==========================================
# 2. Minimal Runtime Stage
# ==========================================
FROM python:3.12-slim AS runtime

ENV DEBIAN_FRONTEND=noninteractive \
    PATH="/app/.venv/bin:$PATH" \
    PYTHONUNBUFFERED=1

# Enable Debian non-free repositories for unrar, then install runtime utilities
RUN ( [ -f /etc/apt/sources.list.d/debian.sources ] && sed -i 's/Components: main/Components: main non-free non-free-firmware/' /etc/apt/sources.list.d/debian.sources || sed -i 's/main$/main non-free/' /etc/apt/sources.list ) \
    && apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
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

WORKDIR /app

# Copy the pre-built virtualenv and uv binary from builder
COPY --from=builder /app/.venv /app/.venv
COPY --from=ghcr.io/astral-sh/uv:latest /uv /bin/

# Copy project configuration and application source code
COPY pyproject.toml uv.lock ./
COPY app ./app

# Create non-privileged runtime user
RUN useradd -m -u 1000 botuser \
    && mkdir -p /app/data /app/logs \
    && chown -R botuser:botuser /app

USER botuser

VOLUME ["/app/data", "/app/logs"]

ENTRYPOINT ["uv", "run", "python", "-m", "app.bot"]
