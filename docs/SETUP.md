# Local Setup

## Required tools

- Go 1.22+
- Docker Desktop with Docker Compose v2
- `golang-migrate` — `brew install golang-migrate`
- `jq` — `brew install jq`
- `openssl` — pre-installed on macOS

## First run

1. Copy `.env.example` to `.env` and fill in real values:
```bash
   cp .env.example .env
   # edit .env, generate secrets per the comments
```
2. Bring up infrastructure: `make up`
3. Run migrations: `make migrate-up`
4. Start the gateway: `make dev` (or `make run` in another terminal)
5. Seed test data: `make seed` (in a third terminal)

## Reset

To wipe everything and start fresh:
```bash
make nuke && make up && make migrate-up && make seed
```
