FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY . ./

WORKDIR /app
RUN go build -o /rinha2025

# Final image
FROM alpine:latest
COPY --from=builder /rinha2025 /rinha2025

EXPOSE 8080

CMD ["/rinha2025"]