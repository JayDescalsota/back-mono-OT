# Backend AI Instructions (ot/back — Go + gqlgen + Bun + Postgres)

## Table naming convention
New tables MUST be named `{service}_{name}`, where `{service}` is the
service that mainly owns the table:
`booking_appointments`, `booking_appointment_types`, `booking_disciplines`,
`tenant_roles`, `tenant_permissions`, `patient_goals`, `messaging_threads`, ...

Legacy exceptions (predate the rule, do not rename without asking):
`patients`, `users`, `apps`, `modules`.

## Backend workflow
- Rebuild + restart the app after any backend change:
  `docker compose build app` then `docker compose up -d app` (workdir: `D:\Code\ot\back`).
- Migrations live in `migrations/` (`NN_NN_name.up.sql` / `.down.sql`) and are
  tracked in `schema_migrations`. Keep one schema + one data migration per
  domain (`NN_01_*_init`, `NN_02_*_data`); fold follow-ups into them instead
  of stacking numbered migrations while pre-production. The live DB is the
  `clinic_postgres` container (`clinic` database); apply new migrations with
  psql and record them in `schema_migrations` like `app.sh` does.

## Conventions
- Bun models mirror tables 1:1 (`services/<svc>/db/models.go`).
- Adding a GraphQL type: schema.graphqls → gqlgen.yml binding →
  `go run github.com/99designs/gqlgen generate` → resolver in
  `graph/schema.resolvers.go`. If generation fails on its own new references,
  temporarily stub the service usage, generate, then restore.
- `services/booking/graph/model/models_gen.go` is a hand-kept mirror of the
  root generated input types — update it in lockstep when inputs change.
- `tenant_permissions` are code-seeded definitions; page modules live in the
  `modules` DB table (queried via `modules`); role→permission LINKS are data.
