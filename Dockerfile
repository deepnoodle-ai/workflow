# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install git for go mod download (some dependencies may need it)
RUN apk add --no-cache git

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build server and worker binaries
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /worker ./cmd/worker

# Server image
FROM alpine:3.19 AS server

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /server /usr/local/bin/server

EXPOSE 8080

ENTRYPOINT ["server"]
CMD ["serve"]

# Worker image
FROM alpine:3.19 AS worker

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /worker /usr/local/bin/worker

ENTRYPOINT ["worker"]
CMD ["run"]
