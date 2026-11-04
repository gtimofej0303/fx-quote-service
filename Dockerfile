# Build
FROM golang:1.27.3-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=0

RUN go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api
RUN go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# Run
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/api /api
COPY --from=builder /out/migrate /migrate

EXPOSE 8080

ENTRYPOINT ["/api"]