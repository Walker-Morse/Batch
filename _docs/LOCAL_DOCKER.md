# Local Docker Compose

Use Docker Compose to run a local Postgres database with the project schema loaded.

## Start local database

```bash
make db-up
```

## Check startup / schema init logs

```bash
make db-logs
```

## Connect with psql

```bash
make db-psql
```

## Load development fixtures

```bash
make db-init-schema
make db-seed
```

This loads deterministic fixture data for tenant `local-dev` across:

- `programs`
- `batch_files`
- `domain_commands`
- `batch_records_rt30`
- `consumers`
- `cards`
- `purses`

Reset fixture data only:

```bash
make db-reset-fixtures
```

Connection details:

- host: `localhost`
- port: `5432`
- db: `onefintech`
- user: `ingest_task`
- password: `dev_password_not_secret`

## Stop containers

```bash
make db-down
```

If you need to re-run schema initialization from scratch:

```bash
docker compose down -v
```

Then:

```bash
make db-up
make db-seed
```

## Optional: run ingest-task container locally

This project includes an `ingest-task` compose service under the `app` profile.

```bash
docker compose --profile app up ingest-task
```

By default it starts with placeholder CLI arguments. Update service command/env in
`docker-compose.yml` for your local file and runtime settings.
