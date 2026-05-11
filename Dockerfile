# syntax=docker/dockerfile:1.7

FROM node:20-alpine AS frontend
WORKDIR /frontend
COPY frontend/.npmrc frontend/package.json frontend/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM golang:1.26-alpine AS backend-go
WORKDIR /src/backend-go
COPY backend-go/go.mod backend-go/go.sum ./
RUN go mod download
COPY backend-go/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tank-operator-go ./cmd/tank-operator

FROM alpine:3.20
RUN adduser -D -u 1000 app
WORKDIR /app
COPY --from=frontend /frontend/dist /app/static
COPY --from=backend-go /out/tank-operator-go /app/tank-operator-go
ENV TANK_OPERATOR_STATIC_DIR=/app/static
EXPOSE 8000
USER 1000
CMD ["/app/tank-operator-go"]
