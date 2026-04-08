# ---- build stage ----
FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /connectivity-exporter .

# ---- final stage ----
FROM scratch

COPY --from=builder /connectivity-exporter /connectivity-exporter

# Run as unprivileged user (UID 65534 = nobody)
USER 65534:65534

EXPOSE 9090

ENTRYPOINT ["/connectivity-exporter"]
