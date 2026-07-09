# Payment Gateway Aggregator - Backend

Backend service untuk platform pembayaran QRIS menggunakan Go.

## Tech Stack

- **Language**: Go 1.23
- **Router**: Gorilla Mux
- **Database**: PostgreSQL
- **Cache/Queue**: Redis (future)
- **Provider**: KlikQris (initial)

## Project Structure

```
backend/
├── cmd/api/              # Application entry point
├── internal/
│   ├── config/          # Configuration management
│   ├── domain/          # Domain models (payment, merchant, provider)
│   ├── handler/         # HTTP handlers
│   ├── service/         # Business logic layer
│   ├── repository/      # Data access layer
│   ├── provider/        # Provider adapters
│   └── middleware/      # HTTP middleware
├── migrations/          # Database migrations
└── pkg/                 # Shared packages
```

## Setup

1. Copy environment file:
```bash
cp .env.example .env
```

2. Update `.env` with your configuration

3. Install dependencies:
```bash
go mod download
```

4. Run migrations:
```bash
# Instructions will be added
```

5. Run the server:
```bash
go run cmd/api/main.go
```

## Development

- The API runs on port 8080 by default
- Supports hot reload with air (optional)
- Webhook endpoint: `/api/v1/provider-webhooks/klikqris`

## Architecture Principles

- Payment orchestration backend (not KlikQris wrapper)
- Provider-agnostic core system
- KlikQris is just one provider adapter
- All provider-specific logic isolated in adapters
- Normalized internal payment status

## Environment

- Development: Local
- Staging: TBD
- Production: TBD
