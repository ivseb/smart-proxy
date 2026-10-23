# Build Stage for Frontend
FROM node:26-alpine AS frontend-builder
WORKDIR /app/web/dashboard
COPY web/dashboard/package*.json ./
RUN npm ci
COPY web/dashboard/ .
RUN npm run build

# Build Stage for Backend. The Go version is pinned (Dependabot keeps it current): security
# fixes of the standard library arrive with patch releases.
FROM golang:1.27.3-alpine AS backend-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static binary (CGO_ENABLED=0), time zones embedded (time/tzdata in cmd/server).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o smart-proxy ./cmd/server
# The final image's files: the binary, CA certificates (OIDC, Thanos…), a writable /tmp. /app is
# group-writable for root's group: OpenShift runs the container with a random UID in group 0.
RUN mkdir -p /rootfs/app/web /rootfs/tmp /rootfs/etc/ssl/certs && \
    cp smart-proxy /rootfs/app/ && \
    cp /etc/ssl/certs/ca-certificates.crt /rootfs/etc/ssl/certs/ && \
    chmod 1777 /rootfs/tmp && \
    chmod -R g=u /rootfs/app

# Final Stage: no operating system at all (no shell, no package manager, no OS packages to have
# CVEs), only Smart Proxy and its dashboard.
FROM scratch
COPY --from=backend-builder /rootfs/ /
COPY --from=frontend-builder /app/web/dashboard/dist /app/web/static
WORKDIR /app

EXPOSE 8080 8081
USER 1001
ENTRYPOINT ["/app/smart-proxy"]
