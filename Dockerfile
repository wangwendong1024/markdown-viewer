FROM node:22-slim AS ui
WORKDIR /src/ui
COPY ui/package.json ui/yarn.lock ./
RUN yarn install --frozen-lockfile --non-interactive
COPY ui/ ./
RUN NODE_OPTIONS=--openssl-legacy-provider yarn build

FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY auth_schema.sql ./
COPY ui/login.html ./ui/login.html
COPY deploy/assets.go.txt ./bindata.go
COPY --from=ui /src/ui/dist ./ui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /markdown-viewer .

FROM alpine:3.22
RUN mkdir -m 0700 /data && chown 65534:65534 /data
COPY --from=builder /markdown-viewer /usr/local/bin/markdown-viewer
USER 65534:65534
ENV AUTH_DB_PATH=/data/auth.db
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:3000/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/markdown-viewer"]
CMD ["/docs"]
