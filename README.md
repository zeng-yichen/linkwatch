Linkwatch

Linkwatch is a simple HTTP service, written in Go, that registers URLs, periodically checks their status, and exposes the results.

## Features

- **URL Registration**: POST /v1/targets to register a new URL for monitoring.
- **Idempotency**: Handles duplicate URL submissions and supports an Idempotency-Key header for safe retries.
- **List Targets**: GET /v1/targets with cursor-based pagination to list all monitored URLs.
- **List Results**: GET /v1/targets/{id}/results to view the recent check history for a specific URL.
- **Background Checking**: A concurrent worker pool periodically checks each URL's status.
- **Per-Host Limiting**: Ensures that no more than one check is ever in-flight for a single host at the same time.
- **Durable Storage**: Uses SQLite (via pure Go `modernc.org/sqlite` driver) for persistent storage of targets and check results.
- **Graceful Shutdown**: Handles SIGTERM signals to finish in-flight work before exiting.

## Running the Service

### Prerequisites

- Go 1.24+

### Running Locally

The service uses SQLite by default for simplicity and zero-configuration deployment:

```bash
go run ./cmd/linkwatch
```

The API will be available at http://localhost:8080.

### Docker (Optional)

You can also run the service using Docker:

```bash
docker build -t linkwatch .
docker run -p 8080:8080 linkwatch
```

## Configuration

The service is configured via environment variables. The following variables are available:

| Variable | Description | Default |
|----------|-------------|---------|
| HTTP_PORT | The port for the API server to listen on. | 8080 |
| DATABASE_URL | The SQLite database file path. | linkwatch.db |
| CHECK_INTERVAL | The interval between checking cycles. | 15s |
| MAX_CONCURRENCY | The max number of concurrent URL checks. | 8 |
| HTTP_TIMEOUT | The timeout for each individual HTTP check. | 5s |
| SHUTDOWN_GRACE | The grace period for shutdown. | 10s |

## API Usage

### Register a URL

```bash
curl -X POST http://localhost:8080/v1/targets \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: my-key-123" \
  -d '{"url": "https://example.com"}'
```

### List Targets

```bash
curl "http://localhost:8080/v1/targets?limit=10"
```

### Get Check Results

```bash
curl "http://localhost:8080/v1/targets/t_123/results?limit=5"
```

### Health Check

```bash
curl http://localhost:8080/healthz
```

## Running Tests

To run the entire test suite:

```bash
go test ./...
```

The tests are self-contained and do not require a running database or any other external dependencies.

## Database

**Note**: This implementation currently supports only SQLite using the pure Go `modernc.org/sqlite` driver. PostgreSQL support is planned but not implemented.

## Architecture

- **Background Checker**: Concurrent worker pool with per-host limiting
- **Storage Layer**: SQLite database with proper indexing and constraints
- **Idempotency**: Two-level idempotency (Idempotency-Key + canonical URL)
- **Pagination**: Cursor-based pagination for stable, efficient results

For detailed design decisions and architecture, see [DESIGN.md](DESIGN.md).