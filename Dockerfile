# Build Stage for Frontend
FROM node:20-alpine AS frontend-builder
WORKDIR /app/web/dashboard
COPY web/dashboard/package*.json ./
RUN npm ci
COPY web/dashboard/ .
RUN npm run build

# Build Stage for Backend
FROM golang:1.24-alpine AS backend-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO_ENABLED=0 for static binary
RUN CGO_ENABLED=0 GOOS=linux go build -o smart-proxy ./cmd/server

# Final Stage
FROM alpine:3.19
WORKDIR /app

# Install ca-certificates for K8s API communication
RUN apk --no-cache add ca-certificates

# Copy binary from backend-builder
COPY --from=backend-builder /app/smart-proxy .

# Copy built frontend assets to the static directory
# We create /app/web/static and copy contents of dist there
COPY --from=frontend-builder /app/web/dashboard/dist ./web/static
COPY --from=backend-builder /app/web/templates ./web/templates

# Expose ports
EXPOSE 8080 8081

# User configuration (OpenShift random user support)
RUN chgrp -R 0 /app && \
    chmod -R g=u /app

USER 1001

CMD ["./smart-proxy"]
