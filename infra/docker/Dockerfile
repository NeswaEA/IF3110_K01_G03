# Build context: repository root. CGO is required (go-sqlite3, pg_query_go), so
# the backend stage uses the full Go image that ships a C compiler.
FROM node:24.22.0 AS web
# the mock handlers (type-checked by the build) import the shared fixtures from ../tests/fixtures/api
WORKDIR /repo/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY tests/fixtures/api /repo/tests/fixtures/api
COPY web/ ./
RUN npm run build

FROM golang:1.27.2-bookworm AS backend
ENV CGO_ENABLED=1 GOTOOLCHAIN=local
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum* ./
RUN go mod download
COPY backend/ ./
RUN go build -o /out/server ./cmd/server

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 app
WORKDIR /app
COPY --from=backend /out/server /app/server
COPY --from=web /repo/web/dist /app/web/dist
USER app
EXPOSE 8080
ENTRYPOINT ["/app/server"]
