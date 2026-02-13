# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o proxy main.go

# Final stage
FROM alpine:latest

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/proxy .

# Expose port 9000
EXPOSE 9000

# Command to run the application
ENTRYPOINT ["./proxy"]
