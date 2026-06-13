# syntax=docker/dockerfile:1.7

FROM node:20-alpine AS frontend
WORKDIR /build/frontend
COPY frontend/.npmrc frontend/package.json frontend/package-lock.json* ./
RUN npm ci --no-audit --no-fund
# runner-shared/ is the single source for the Tank conversation contract;
# the frontend imports it via the relative path ../../runner-shared/
# conversation.js. Without this layer the relative import resolves outside
# the build context and tsc fails to find the module.
COPY runner-shared/ /build/runner-shared/
COPY frontend/ ./
RUN npm run build

FROM golang:1.26-alpine AS backend-go
WORKDIR /src/backend-go
COPY backend-go/go.mod backend-go/go.sum ./
RUN go mod download
COPY backend-go/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tank-operator-go ./cmd/tank-operator
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tank-supervisor ./cmd/tank-supervisor
# Per-session NATS credential issuer (#1128). Rides the app image so it
# deploys/rolls back in lockstep with the orchestrator that owns the
# session subject contract; the chart runs it as its own Deployment with
# command: /app/nats-auth-callout.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/nats-auth-callout ./cmd/nats-auth-callout

FROM alpine:3.20
RUN adduser -D -u 1000 app
WORKDIR /app
COPY --from=frontend /build/frontend/dist /app/static
COPY --from=backend-go /out/tank-operator-go /app/tank-operator-go
COPY --from=backend-go /out/tank-supervisor /app/tank-supervisor
COPY --from=backend-go /out/nats-auth-callout /app/nats-auth-callout
ENV TANK_OPERATOR_STATIC_DIR=/app/static
EXPOSE 8000
USER 1000
CMD ["/app/tank-operator-go"]
