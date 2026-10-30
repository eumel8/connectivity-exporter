# ---- build stage ----
FROM golang:1.27.3-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN apk add --no-cache ca-certificates
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /connectivity-exporter .

# ---- final stage ----
FROM scratch

COPY --from=builder /connectivity-exporter /connectivity-exporter
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Run as unprivileged user (UID 65534 = nobody)
USER 65534:65534

EXPOSE 9090

ENTRYPOINT ["/connectivity-exporter"]
