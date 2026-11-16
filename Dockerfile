# syntax=docker/dockerfile:1

# ---- UI ----
FROM node:22-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN mkdir -p ../internal/web && npm run build

# ---- Server ----
FROM golang:1.26-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=ui /src/internal/web/dist ./internal/web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/simplepanel ./cmd/simplepanel

# ---- Runtime ----
# Alpine provides git (for Git deploys) and CA certificates.
FROM alpine:3.22
RUN apk add --no-cache git ca-certificates tzdata
COPY --from=server /out/simplepanel /usr/local/bin/simplepanel
VOLUME /data
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:3000/api/health >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/simplepanel"]
