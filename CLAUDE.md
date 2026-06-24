# platform

API gateway untuk ekosistem Avagenc. Menerima request dari client, autentikasi via JWT, lalu meneruskan ke agent backend atau menangani domain sendiri.

## Struktur

```
cmd/platform/main.go        — entrypoint, wire-up semua dependency
internal/agent/             — reverse proxy ke agent backend (ava, zee)
internal/identity/          — JWT autentikasi + payment guard middleware
internal/postera/           — handler untuk scheduled messages (postera domain)
memory/                     — package PUBLIC: ports (SessionStore, KnowledgeStore) + tipe domain + ErrNotFound
internal/memory/            — handler + service (ownership/authz, ErrForbidden) yang mendorong ports
internal/zep/               — adapter Zep yang implement ports di memory/ (satu-satunya yang import Zep)
```

Catatan idiom: `memory/` sengaja public (di luar `internal/`) supaya `internal/zep` bisa
mengimplementasikannya tanpa ada satu package internal yang mengimport package internal lain.
`internal/memory` dan `internal/zep` sama-sama hanya bergantung pada `memory/`, bukan satu sama lain.

## Domain

**agent** — reverse proxy ke ava dan zee. Propagate `user-id`, `time-zone`, `session-id` ke upstream via header. Zee pakai OIDC transport (GCP idtoken), ava tidak.

**postera** — handler langsung pakai `*postera.Postarius` (tidak ada service layer). Postarius sendiri adalah orchestrator yang scope-aware via context. Auth gate di handler sebelum memanggil Postarius.

**zep** — punya service layer karena ada ownership check manual: `Thread.Get` dulu, bandingkan `UserID`, baru `Thread.Delete`. Zep tidak scope-aware seperti Postarius.

- `/sessions/{id}/messages` — GET/DELETE pesan satu thread. Butuh ownership check karena `threadID` dari URL bisa milik siapa saja.
- `/memory` — GET/DELETE knowledge graph user. Tidak butuh ownership check karena operasi sudah di-scope ke `userID` dari JWT (`GetByUserID`, `User.Delete`). **DELETE `/memory` memanggil `User.Delete` di Zep yang menghapus seluruh data user termasuk semua threads/sessions — behavior ini disengaja.**

**identity** — `JWTAuthenticator` middleware extract `sub` dari JWT, simpan ke context via `user.ContextWithID`. `PaymentGuard` cek Redis set `users:blocked:payment`.

## Auth flow

Semua route di bawah group middleware `jwtAuthenticator.Authenticate`. User ID tersedia di context via `apiuser.IDFromContext`.

## Environment variables

| Var | Keterangan |
|-----|-----------|
| `IDENTITY_JWKS_URL` | JWKS endpoint untuk verifikasi JWT |
| `ZEP_API_KEY` | API key Zep |
| `AVA_URL` | URL backend ava |
| `ZEE_URL` | URL backend zee (OIDC) |
| `POSTERA_DB_URL` | PostgreSQL connection string untuk postera |
| `GCP_PROJECT_ID` | GCP project ID |
| `CLOUD_TASKS_LOCATION_ID` | Cloud Tasks location |
| `CLOUD_TASKS_QUEUE_ID` | Cloud Tasks queue ID |
| `APP_ENV` | `production` atau `development` |
| `PORT` | Port server (default `8080`) |

## Conventions

- Handler thin: hanya HTTP glue (extract param, map error ke status code)
- Service layer hanya kalau ada logika bisnis nyata (ownership check, multi-step orchestration)
- Tidak ada mock testing — integration test untuk behavior, bukan unit test handler
- `go.naturallyfunny.dev/api` menyediakan helper context (user, session, time) dan HTTP utilities
- Tidak ada nama file stutter (`pkg/pkg.go`, mis. `memory/memory.go`, `zep/zep.go`). Pisah file per
  konsep (mis. `session.go`, `knowledge.go`). Doc comment package + deklarasi yang benar-benar
  lintas-file (sentinel/tipe shared, helper bersama) taruh di `doc.go`.
