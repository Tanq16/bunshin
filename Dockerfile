FROM golang:alpine AS builder
WORKDIR /app
COPY . .
RUN go build -ldflags="-s -w" -o bunshin .

FROM alpine:latest
WORKDIR /app
RUN mkdir -p /app/data/stacks /app/data/env
COPY --from=builder /app/bunshin .
EXPOSE 8080
CMD ["/app/bunshin", "--data", "/app/data"]
