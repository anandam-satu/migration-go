# api-migration (Go port)

Go port of the Spring Boot `api-migration` worker (Java sources stay in
`../src`). It migrates data from the legacy **MySQL** ("MyBiz") database into
the **PostgreSQL** (`stok-anandam`) database, exposes HTTP trigger endpoints,
and runs a background auto-sync scheduler.

The **pricelist feature was intentionally removed** (see "Removed" below).
Everything else in the Java module was ported.

## Layout

| Go file | Java counterpart |
|---|---|
| `cmd/server/main.go` | `ApiMigrationApplication.java` |
| `internal/config` | `application.properties` + `*Config.java` value bindings |
| `internal/db` | `MysqlConfig`/`PostgresConfig`/`LegacyDbConfig` (Hikari pools, DSNs), Hibernate `ddl-auto=update` schema bootstrap, `DatabaseMigrationConfig` constraint fix |
| `internal/model` | `core/postgres/model/*` (minus `Pricelist.java`) |
| `internal/normalize` | `util/NormalizationUtil.java` |
| `internal/service` | `service/MigrationService.java` (minus all pricelist/Google Sheets code) |
| `internal/server` | `controller/MigrationController.java`, `service/MigrationScheduler.java` |
| `internal/repository` | `repository/{Purchase,Sales,Stock,SyncSettings}Repository.java` read/aggregation queries (minus pricelist) |

## Configuration

Environment variables are the same ones `application.properties` consumed:

| Env | Default | Meaning |
|---|---|---|
| `APP_MYSQL_ENABLED` | `true` | when `false`, trigger endpoints return 503 and the scheduler is disabled |
| `DB_PG_HOST` / `DB_PG_PORT` / `DB_PG_NAME` | `192.168.1.176` / `5432` / `stok-anandam` | PostgreSQL sink |
| `DB_PG_USER` / `DB_PG_PASSWORD` | `anandamstok` / `Letmein99+` | PostgreSQL credentials |
| `DB_MYSQL_HOST` / `DB_MYSQL_PORT` / `DB_MYSQL_NAME` | `192.168.1.246` / `3307` / `anandamid26` | legacy MySQL source |
| `DB_MYSQL_USER` / `DB_MYSQL_PASSWORD` | `root` / *(empty)* | MySQL credentials |
| `MIGRATION_LEGACY_SCHEMA` | `anandamid26` | informational (source SQL is ported verbatim) |
| `SERVER_PORT` | `9089` | HTTP listen port |

## Endpoints

- `POST /api/v1/migration/purchase`
- `POST /api/v1/migration/sales`
- `POST /api/v1/migration/stock`
- `POST /api/v1/migration/sn`

Each kicks off the corresponding migration in the background (like `@Async`)
and answers `200 {"status":200,...}`; when MySQL is disabled it answers
`503 {"status":503,...}` — same shape as the Java controller.

## Background sync

Every 30 s (`MigrationScheduler` parity) the service checks `MAX(id)` of the
legacy `dbslog` table against the watermark stored in `sync_settings`
(`last_max_id`). If new rows exist it runs stock → sales → purchase → SN and
updates the watermark. The nightly quiet window (21:15–07:55 local) is
respected. After each cycle, idle MySQL connections are dropped (no Sleep
connections left on MyBiz), mirroring Hikari `softEvictConnections`.

## Migration behavior notes (parity)

- Reads are streamed from MySQL and upserted to PostgreSQL in batches of
  1000 with a single multi-row `INSERT … ON CONFLICT (…) DO UPDATE`, keeping
  `nextval('purchase_seq'|'sales_seq'|'stok_seq')`.
- Each job stamps `last_synced` with the run start time, then deletes rows
  with an older `last_synced` (stale data from previous runs).
- Dates travel as `YYYY-MM-DD`, timestamps as `YYYY-MM-DD HH:MM:SS.ffffff`,
  money as their text representation (never through float64).
- The four source SQL queries are byte-for-byte the Java ones (`%%` LIKE
  patterns simplified to `%`, which is equivalent).

## Removed (pricelist feature)

`syncStockPricelistFromSheet` and helpers, Google Sheets credentials/reads,
`Pricelist` entity, `PricelistRepository`, `PricelistNormalizationRunner`,
`StockRepository.findDistinctItemCodesSortedByPricelist`, the pricelist
`@Transient` fields on `Stock`, and the pricelist parsing unit test. Nothing
in the remaining app referenced them.

## Repository read queries

The `internal/repository` package ports the JPA query methods of
`PurchaseRepository`, `SalesRepository`, `StockRepository` and
`SyncSettingsRepository` as plain DAO functions. As in the Java module these
queries are not wired to any endpoint in this worker; they exist for parity
with the original codebase. Numeric columns are selected via `::text` and
parsed with `shopspring/decimal` so behaviour does not depend on the pgx
codec. Pagination uses `LIMIT/OFFSET` with a deterministic `ORDER BY`.

## Build & test

```sh
cd go
go build ./...
go vet ./...
go test ./...
```

The server is started with:

```sh
go run ./cmd/server
```
