# Stage 1: Build static binary
FROM golang:1.24-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /spark ./cmd/spark

# Stage 2: Minimal runtime image
FROM scratch

COPY --from=builder /spark /spark

ENTRYPOINT ["/spark"]
