# Build
# Use Go on Alpine Linux to build the API and migration programs.
FROM golang:1.27.3-alpine AS builder

# Use /app inside the build image for source files and build commands.
WORKDIR /app

# Copy go.mod and go.sum from the local project folder into /app first.
COPY go.mod go.sum ./
# Download Go dependencies.
RUN go mod download

# Copy the local project files into /app, except files listed in .dockerignore.
COPY . .

# Disable C library support so both programs can run in the small runtime image.
ENV CGO_ENABLED=0

# Build the HTTP API from cmd/api and save it as /out/api.
# Remove local source paths (-trimpath) and debug data (-s -w).
RUN go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api
# Build the database migration tool from cmd/migrate as /out/migrate.
# Use the same options to remove local paths and reduce the file size.
RUN go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate


# Run
# Start a small final image without Go tools or a shell.
FROM gcr.io/distroless/static-debian12:nonroot

# Copy the API program from the builder stage to /api in the final image.
COPY --from=builder /out/api /api
# Copy the migration tool from the builder stage to /migrate.
COPY --from=builder /out/migrate /migrate

# Declare the API port. Compose maps it to a port on the host computer.
EXPOSE 8080

# Run /api by default when a container starts from this image.
ENTRYPOINT ["/api"]
