FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY shared/ ./shared/
COPY services/ ./services/
COPY graph/ ./graph/
COPY cmd/ ./cmd/
RUN CGO_ENABLED=0 GOOS=linux go build -o server -ldflags="-s -w" ./cmd/server

FROM alpine:3.22
WORKDIR /app
COPY --from=builder /app/server .
EXPOSE 4000
CMD ["./server"]
