# Build context: repository root. CGO is required (go-sqlite3, pg_query_go), so
# the backend stage uses the full Go image that ships a C compiler.
FROM node:24.22.0@sha256:14078204b6bb268c107ceff6d3c43f45a888105e5c2b18b14c32b3409d93a1b0 AS web
# the mock handlers (type-checked by the build) import the shared fixtures from ../tests/fixtures/api
WORKDIR /repo/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY tests/fixtures/api /repo/tests/fixtures/api
COPY web/ ./
RUN npm run build

FROM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS backend
ENV CGO_ENABLED=1 GOTOOLCHAIN=local
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum* ./
RUN go mod download
COPY backend/ ./
RUN go build -o /out/server ./cmd/server

FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
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
