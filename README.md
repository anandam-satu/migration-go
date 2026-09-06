# api-migration (Go port)

Go port of the Spring Boot `api-migration` worker (Java sources stay in
`../src`). It migrates data from the legacy **MySQL** ("MyBiz") database into
the **PostgreSQL** (`stok-anandam`) database, keeps **public.pricelist** in
sync with a Google Sheet, exposes HTTP trigger endpoints, and runs background
auto-sync schedulers.

## Layout

| Go file | Java counterpart |
|---|---|
| `cmd/server/main.go` | `ApiMigrationApplication.java` |
| `internal/config` | `application.properties` + `*Config.java` value bindings |
| `internal/db` | `MysqlConfig`/`PostgresConfig`/`LegacyDbConfig` (Hikari pools, DSNs), Hibernate `ddl-auto=update` schema bootstrap, `DatabaseMigrationConfig` constraint fix |
| `internal/model` | `core/postgres/model/*` |
| `internal/normalize` | `util/NormalizationUtil.java` |
| `internal/sheet` | Google Sheets API usage in `MigrationService` (service-account, `values().get`) |
| `internal/service` | `service/MigrationService.java` (incl. `syncStockPricelistFromSheet`) |
| `internal/server` | `controller/MigrationController.java`, `service/MigrationScheduler.java` |
| `internal/repository` | `repository/{Purchase,Sales,Stock,SyncSettings}Repository.java` read/aggregation queries |

## Configuration

Environment variables are the same ones `application.properties` consumed.
They can be set the usual way (shell/launcher) **or** in a `.env` file: at
startup the worker loads the file named by `ENV_FILE` (default `.env` in the
current working directory) before reading the configuration. Copy
`.env.example` and fill it in — it carries the MySQL, PostgreSQL and Google
pricelist settings in the table below (the `ENV_FILE` loader knob itself must
be a real environment variable). Variables already present in the process
environment always win over the file, and `.env` is gitignored. A `.env` that
fails to parse — or an explicitly set `ENV_FILE` that points at a file which
does not exist — stops startup with an error instead of silently running on
default credentials (which point at real hosts).

| Env | Default | Meaning |
|---|---|---|
| `ENV_FILE` | `.env` | path of the `.env` file loaded at startup (process env still wins over it) |
| `APP_MYSQL_ENABLED` | `true` | when `false`, trigger endpoints return 503 and the MySQL scheduler is disabled |
| `DB_PG_HOST` / `DB_PG_PORT` / `DB_PG_NAME` | `192.168.1.176` / `5432` / `stok-anandam` | PostgreSQL sink |
| `DB_PG_USER` / `DB_PG_PASSWORD` | `anandamstok` / `Letmein99+` | PostgreSQL credentials |
| `DB_MYSQL_HOST` / `DB_MYSQL_PORT` / `DB_MYSQL_NAME` | `192.168.1.246` / `3307` / `anandamid26` | legacy MySQL source |
| `DB_MYSQL_USER` / `DB_MYSQL_PASSWORD` | `root` / *(empty)* | MySQL credentials |
| `MIGRATION_LEGACY_SCHEMA` | `anandamid26` | informational (source SQL is ported verbatim) |
| `SERVER_PORT` | `9089` | HTTP listen port |
| `GOOGLE_SERVICE_ACCOUNT_FILE` | *(unset → pricelist sync off)* | path to the service-account JSON (scope `spreadsheets.readonly`) shared with the pricelist sheet |
| `PRICELIST_SPREADSHEET_ID` | `173w5Y8hynv8lOphrsjtCx0tc8CJIQThuLrMIttwtw30` | spreadsheet read by the pricelist sync |
| `PRICELIST_SHEET_RANGE` | `'PRICELIST&MODAL'!A:D` | values range on the sheet; normally leave unset. If set in `.env`, the value must be double-quoted (`PRICELIST_SHEET_RANGE="'PRICELIST&MODAL'!A:D"`) — an unquoted `'...'!A:D` is a `.env` parse error |
| `PRICELIST_SYNC_INTERVAL` | `1m` | how often the sheet is pulled (Go duration, e.g. `1m`, `60s`) |

## Endpoints

- `POST /api/v1/migration/purchase`
- `POST /api/v1/migration/sales`
- `POST /api/v1/migration/stock`
- `POST /api/v1/migration/sn`
- `POST /api/v1/migration/pricelist`

The purchase/sales/stock/sn endpoints kick off the corresponding migration in
the background (like `@Async`) and answer
`200 {"status":200,...}`; when MySQL is disabled they answer
`503 {"status":503,...}` — same shape as the Java controller. The pricelist
endpoint triggers a sheet sync immediately; it answers `503` only when
`GOOGLE_SERVICE_ACCOUNT_FILE` is not set (pricelist sync disabled).

## Background sync

- Every 30 s (`MigrationScheduler` parity) the service checks `MAX(id)` of the
  legacy `dbslog` table against the watermark stored in `sync_settings`
  (`last_max_id`). If new rows exist it runs stock → sales → purchase → SN and
  updates the watermark. The nightly quiet window (21:15–07:55 local) is
  respected. After each cycle, idle MySQL connections are dropped (no Sleep
  connections left on MyBiz), mirroring Hikari `softEvictConnections`.
- Every `PRICELIST_SYNC_INTERVAL` (default **1 minute**, no quiet window) the
  worker pulls the `PRICELIST&MODAL` tab of the Google Sheet and mirrors it
  into **public.pricelist** — see "Pricelist sync" below.

## Pricelist sync

Reads the tab `PRICELIST&MODAL` (columns A=spesifikasi, B=item_name, C=modal,
D=final_pricelist; no header row, data starts at row 1) of spreadsheet
`173w5Y8hynv8lOphrsjtCx0tc8CJIQThuLrMIttwtw30` using the service account from
`GOOGLE_SERVICE_ACCOUNT_FILE`, and mirrors it into `public.pricelist`:

- rows are keyed by `normalized_item_name` (`normalize.NormalizeItemName` of
  the raw B cell, so `NB ACER AL14-37P-32RZ` and `NB ACER AL14 37P 32RZ`
  collide);
- money cells are parsed with the Java worker's `cleanBigDecimal` rules
  (Indonesian formats: `"2.857.000"`, `"2.857.000,50"`, `"Rp 2.857.000"`);
- each row is `INSERT`ed (via `nextval('pricelist_seq')`) or `UPDATE`d when
  `normalized_item_name` already exists — the column is only indexed, not
  unique, so no `ON CONFLICT` is used;
- rows whose `normalized_item_name` is no longer on the sheet (or is NULL)
  are deleted in the same transaction, so the table always mirrors the sheet.
  As a safety guard, a successful fetch that yields **no** data rows leaves
  the table untouched and logs a warning instead of wiping it.

Each run happens inside one transaction; concurrent runs (e.g. a manual HTTP
trigger while the scheduler ticks) are serialized in-process. The sync must
therefore run from a **single worker instance** (the UPDATE-then-INSERT relies
on one writer; the live column is only indexed, not unique). The schema
(table, `pricelist_seq`, indexes, idempotent `ALTER`s, and a startup
`setval` that aligns the sequence with existing rows) is bootstrapped at
startup like the other tables.

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
# Optional: create your config once, then it is reused on every start.
cp .env.example .env   # fill in the values (MySQL/PostgreSQL/Google)

go run ./cmd/server
```

To use a config file somewhere else, point `ENV_FILE` at it. To enable the
pricelist sync, make sure `GOOGLE_SERVICE_ACCOUNT_FILE` in the file points at a
service-account JSON that has been granted access to the spreadsheet, e.g. in
`.env`:

```
GOOGLE_SERVICE_ACCOUNT_FILE=C:/secrets/service_account.json
```
