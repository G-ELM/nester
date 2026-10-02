# vaultsnapshot

Periodically exports a signed, point-in-time snapshot of every vault's
balance to storage independent of the primary Postgres database, so there's
an immutable record to reconcile against if the primary store is ever
compromised or corrupted.

## How it works

1. `Build` fetches every vault's balance via `Fetcher` and aggregates a
   per-currency total — a `Snapshot`.
2. `Sign` HMAC-SHA256s the snapshot's canonical JSON encoding (same
   primitive this codebase already uses for webhook signing, see
   `service.SignWebhookPayload`), producing an `Envelope` — the pair of
   `{snapshot, signature}` that's actually exported.
3. `Job.Run` does this on a ticker, gated to a single leader replica (same
   pattern as every other alert/export-sending job — see
   `scheduler.APYDeviationJob`), and hands the envelope to an `Exporter`.

**Verifying a snapshot later** (forensic recovery, or just confirming cold
storage hasn't drifted): read the file back, `json.Unmarshal` it into an
`Envelope`, and call `vaultsnapshot.Verify(signingKey, envelope)`. `false`
with no error means the bytes were altered after signing; `true` means they
weren't — reconcile `envelope.Snapshot.Vaults`/`TotalByCurrency` against
whatever the primary database says now.

## Exporters ("cold storage")

- `LocalExporter` — writes to a directory, refusing to overwrite
  (`O_EXCL`) and chmod'ing the file read-only afterward. "Cold storage"
  here is whatever `Dir` resolves to: point it at a path that's genuinely
  independent of the primary database's own disk — a separate volume, an
  NFS/EFS mount, or a cloud bucket **mounted locally** via
  [`s3fs`](https://github.com/s3fs-fuse/s3fs-fuse)/[`rclone mount`](https://rclone.org/commands/rclone_mount/)/[`gcsfuse`](https://github.com/GoogleCloudPlatform/gcsfuse).
  That's a deliberate choice: it gets snapshots into real S3/GCS storage
  without this module taking on an AWS/GCP SDK dependency.
- `HTTPPutExporter` — PUTs to a URL your own `URLFunc` resolves (typically a
  presigned upload URL) — works against any S3-compatible bucket, R2,
  Backblaze B2, or a self-hosted MinIO/garage gateway, generated however you
  prefer (the cloud CLI run as a sidecar, a small internal signing
  service). Not wired into `main.go` by default — add it alongside
  `LocalExporter` in a `MultiExporter` once you have a URL-signing path.
- `MultiExporter` — fans out to several `Exporter`s and requires at least
  `MinSuccess` to succeed, so one target being briefly down doesn't lose the
  whole snapshot (or silently drop the redundancy without anyone noticing —
  a partial failure still returns an error naming what failed).

A native AWS S3/GCS SDK `Exporter` is a reasonable follow-up once one of
those SDKs is added as a proper `go get`-pinned dependency; it's left out
here deliberately rather than hand-rolling SigV4/OAuth signing.

## Configuration

Read directly via `os.Getenv` in `cmd/api/main.go` (same convention as
`scheduler.APYDeviationJobFromEnv`), not through the central config struct:

| Env var | Default | Meaning |
|---|---|---|
| `VAULT_SNAPSHOT_ENABLED` | `true` iff the signing key and export dir are both set | Force on/off. |
| `VAULT_SNAPSHOT_INTERVAL_MINUTES` | `60` | How often `Job` exports. |
| `VAULT_SNAPSHOT_SIGNING_KEY` | unset | HMAC key. **Required** — `Job.Run` refuses to start without one rather than exporting unsigned snapshots. Keep it out of the cold-storage target itself. |
| `VAULT_SNAPSHOT_DIR` | unset | `LocalExporter.Dir`. See "Exporters" above for pointing this at real cold storage. |

## What counts as "ledger state" here

Each vault's own ledger fields at snapshot time: `current_balance`,
`total_deposited` (lifetime inflow), `yield_earned`, `fees_paid`, and
`status`. Full transaction history is what the primary database and
`internal/audit`'s tamper-evident chain are for; this snapshot exists
specifically for when those are unavailable or suspect, so it stays to the
minimum needed to reconcile "what should the balances be" rather than
duplicating the whole ledger.
