# Backend (Go) Dockerfile
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Install dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod ./
# Copy all source files first to run go mod tidy
COPY . .

# Download dependencies and tidy
RUN go mod download
RUN go mod tidy

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./cmd

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates \
    && addgroup -S app && adduser -S -G app app

WORKDIR /app

# Copy the binary from builder
COPY --from=builder /app/main .

# Copy migrations folder
COPY --from=builder /app/migrations ./migrations

# Never run as root
USER app

EXPOSE 8080 8081 9000

CMD ["./main"]
