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
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/tank-operator-go ./cmd/tank-operator

FROM python:3.12-slim AS backend
WORKDIR /app
ENV PYTHONUNBUFFERED=1 PIP_DISABLE_PIP_VERSION_CHECK=1
COPY backend/pyproject.toml ./
COPY backend/src ./src
RUN pip install --no-cache-dir .
COPY --from=frontend /frontend/dist /app/static
COPY --from=backend-go /out/tank-operator-go /app/tank-operator-go
ENV TANK_OPERATOR_STATIC_DIR=/app/static
EXPOSE 8000
USER 1000
CMD ["python", "-m", "tank_operator"]
