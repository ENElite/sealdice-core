# syntax=docker/dockerfile:1

FROM golang:1.25 AS source
ENV GOMAXPROCS=4
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
COPY ui/package.json ui/pnpm-lock.yaml ui/pnpm-workspace.yaml ./
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

FROM golang:1.25
ENV DB_TYPE=sqlite TZ=Asia/Shanghai
RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends tzdata && \
    ln -fs /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    rm -rf /var/lib/apt/lists/*
COPY --chmod=0755 --from=builder /out/sealdice-core /sealdice/sealdice-core
VOLUME ["/sealdice/data", "/sealdice/backups"]
EXPOSE 3211
WORKDIR /sealdice
CMD ["/sealdice/sealdice-core", "--container-mode"]
