# syntax=docker/dockerfile:1

# Keep CGO and runtime glibc compatible.
FROM golang:1.25-bookworm AS source
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /sealdice
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go generate ./... && \
    go run . --gen-openapi=/sealdice/ui/openapi.json

FROM node:22-bookworm-slim AS ui
RUN corepack enable && corepack prepare pnpm@10.14.0 --activate
WORKDIR /sealdice/ui
COPY ui/package.json ui/pnpm-lock.yaml ./
COPY ui/scripts/require-pnpm.mjs ./scripts/require-pnpm.mjs
RUN --mount=type=cache,target=/pnpm/store \
    pnpm install --frozen-lockfile --store-dir=/pnpm/store
COPY ui/ ./
COPY --from=source /sealdice/ui/openapi.json ./openapi.json
RUN pnpm run generate-client && pnpm run build:embed:prepared

FROM source AS builder
COPY --from=ui /sealdice/static/v2ui/dist ./static/v2ui/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -o /out/sealdice-core .

FROM node:22-bookworm-slim AS codex
RUN npm install --global @openai/codex@0.157.1

FROM python:3.13.5-slim-bookworm
RUN apt-get update && \
    apt-get install -y --no-install-recommends bash ca-certificates git libstdc++6 tzdata && \
    rm -rf /var/lib/apt/lists/*
ENV TZ=Asia/Shanghai \
    DB_TYPE=sqlite \
    CODEX_HOME=/codex/home \
    PYTHONUNBUFFERED=1
COPY --from=codex /usr/local/bin/node /usr/local/bin/node
COPY --from=codex /usr/local/lib/node_modules/@openai /usr/local/lib/node_modules/@openai
RUN ln -s /usr/local/lib/node_modules/@openai/codex/bin/codex.js /usr/local/bin/codex
COPY docker/imagegen-requirements.lock.txt /opt/codex-imagegen-requirements.lock.txt
RUN python -m venv /opt/codex-imagegen-venv && \
    /opt/codex-imagegen-venv/bin/pip install --no-cache-dir --only-binary=:all: \
        -r /opt/codex-imagegen-requirements.lock.txt
COPY --chmod=0755 --from=builder /out/sealdice-core /sealdice/sealdice-core
COPY --chmod=0755 docker/entrypoint.sh /usr/local/bin/sealdice-entrypoint
VOLUME ["/sealdice/data", "/sealdice/backups", "/codex"]
EXPOSE 3211
WORKDIR /sealdice
ENTRYPOINT ["/usr/local/bin/sealdice-entrypoint"]
CMD ["/sealdice/sealdice-core", "--container-mode"]
