# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

`github.com/MarceloBaeza/libraries-golang-toolkit` is a shared Go library (not a runnable app) providing small, independent infrastructure utilities for other Go services to import: Postgres connection pooling, SQL migrations, password hashing, and struct validation. There is no `main` package — nothing in this repo runs on its own.

## Commands

```bash
go build ./...      # compile all packages
go vet ./...         # static analysis
go test ./...         # run tests (no _test.go files exist yet — write tests as packages gain behavior)
go mod tidy           # sync go.mod/go.sum after adding/removing imports
```

There is no Makefile, linter config, or CI pipeline in this repo — the above `go` toolchain commands are the only tooling.

## Architecture

The repo is a flat set of independent, single-purpose packages under the module root. Packages do not import each other; each is meant to be imported a la carte by downstream services. (`audit` and `storage` both happen to compose naturally with `dbpool`'s output type — `dbpool.New`'s `*pgxpool.Pool` is what `audit.New` expects — but neither imports `dbpool` itself; the caller wires them together.)

- **`audit`** — `New(pool *pgxpool.Pool) *Recorder` wraps an already-connected pool (e.g. from `dbpool.New`) with no connection logic of its own. `Recorder.Record(ctx, Entry) error` inserts one row into a caller-provisioned `audit_logs` table (schema documented in the package doc comment — this package does not create or migrate it). `Entry`'s string fields (`UserID`, `EntityID`, `IPAddress`, `UserAgent`) become SQL `NULL` when left empty rather than empty strings; `Result` defaults to `ResultSuccess` when unset. The SQL-argument-building logic is split into an unexported, DB-free `argsFor` for unit testing — in particular that a value which fails `json.Marshal` surfaces as an error instead of silently becoming `NULL`.
- **`dbpool`** — `New(ctx, Settings) (*pgxpool.Pool, error)` builds and pings a `pgx/v5` connection pool from a `Settings` struct (DSN, pool size/lifetime knobs). Holds no package-level state, so callers own the pool's lifecycle (memoization, `Close()`, etc.) and multiple independent pools can coexist in one process. `StatementCacheCap` defaults to 256 when unset.
- **`migrations`** — `Run(dsn, migrationsDir string) error` applies pending `golang-migrate` "up" migrations against Postgres using `database/sql` + `lib/pq` (not pgx — migrate's postgres driver requires `database/sql`). On failure it attempts `m.Down()` before returning the error; it does not call `log.Fatalf`/`os.Exit` itself, leaving fail-fast behavior to the caller.
- **`security`** — thin bcrypt wrapper: `HashPassword` / `ComparePassword`, both `[]byte`-casting wrappers around `golang.org/x/crypto/bcrypt` with `bcrypt.DefaultCost`.
- **`storage`** — `New(ctx, Settings) (*Client, error)` connects to an S3-compatible bucket (AWS S3, MinIO, Garage, ...) via `minio-go` and verifies the bucket exists, mirroring `dbpool`'s pattern (no package-level state, no internal retry — callers own the `Client`'s lifecycle and retry policy). `Client.Upload(ctx, UploadInput) (id string, err error)` stores content under a generated key (`folder/.../<uuid8>-<slugified-name>`, prefixed to avoid same-name collisions silently overwriting) and returns only the key — `Name`/`MimeType`/size are unchanged echoes of the input, not worth returning. `Client.Download(ctx, id) ([]byte, error)` reads it back, wrapping "not found" as `ErrObjectMissing` (checkable via `errors.Is`) distinctly from any other failure. `Settings.InsecureHTTP` (not `UseSSL`) is deliberately named so its zero value keeps TLS on. `UploadInput.MimeType` is trusted as given and stored as the object's Content-Type — callers proxying uploads back to a browser must not forward an unchecked caller-supplied MimeType (stored-XSS risk) without forcing `Content-Disposition: attachment` or allow-listing it.
- **`validation`** — a small rule-runner built on `go-playground/validator/v10`:
  - `Validatable` interface (`Validate(v *validator.Validate) *FieldError`) is the contract a domain type implements to plug into the framework.
  - `RunValidations(s any, rules []ValidationRule) *ValidationResult` executes a slice of `ValidationRule` functions against a struct, collecting per-field errors keyed by `FieldError.Field` into `ValidationResult.Errors` (`map[string]string`); `Success` is true only if no rule produced an error.
  - The package-level `validate = validator.New()` instance is shared across all calls — it is not safe to reconfigure (e.g., registering custom validators) concurrently from importing services without coordination.

## Notes for changes

- Keep packages independent — do not introduce cross-package imports between `audit`, `dbpool`, `migrations`, `security`, `storage`, and `validation` unless the design intentionally changes. (`audit` and `storage` accepting a `*pgxpool.Pool`/`Settings` from the caller, rather than importing `dbpool` to build one themselves, is what keeps this holding.)
- `migrations` intentionally uses `database/sql`/`lib/pq` rather than `pgx` because `golang-migrate`'s postgres driver requires it; don't "fix" this to use pgx without checking migrate's driver support first.
- The `migrations` directory currently contains only `migrations.go` — no actual `.sql` migration files exist in this repo yet; `migrationsDir` is a path supplied by the caller at runtime.
