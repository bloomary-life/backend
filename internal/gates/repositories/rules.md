# LinkedIn Account SQLite Repository Sample

Source reviewed: an internal reference implementation of the same repository pattern.

Observed repository pattern:

- one repository package per persistence concern;
- constructor owns query/table constants or builders;
- public methods accept existing domain/main structs, not HTTP DTOs;
- persistence rows are scanned at the gate edge;
- mutations use `context.Context`, parameterized SQL, and transactions for multi-table changes;
- not-found is normalized at repository boundary;
- SQL errors include operation context.

SQLite translation for Bloomary:

- use `database/sql` with `?` placeholders;
- keep `db.SetMaxOpenConns(1)` for SQLite writer safety;
- expose account repository methods by analogy with compliance-auditor repositories: `Insert`, `ByMemberID`, `Delete`;
- use `INSERT ... ON CONFLICT DO UPDATE` for account insert/upsert;
- keep `daily` (quota + post count) in its own gate repository;
- keep this as a sample until the current `package main` structs are moved/exported.
