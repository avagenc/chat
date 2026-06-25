# platform

API gateway untuk ekosistem Avagenc. Menerima request dari client, autentikasi via JWT, lalu meneruskan ke agent backend atau menangani domain sendiri.

## Struktur

```
cmd/platform/main.go        — entrypoint, wire-up semua dependency
internal/agent/             — reverse proxy ke agent backend (ava, zee)
internal/identity/          — JWT autentikasi + payment guard middleware
memory/                     — package PUBLIC: ports (SessionStore, KnowledgeStore) + tipe domain + sentinel per fitur (ErrSessionNotFound, ErrKnowledgeNotFound). Tidak import apa pun yang internal.
internal/memory/            — HTTP handler + service yang mendorong ports. Vertical slice per fitur: session.go & knowledge.go (handler + service masing-masing); handler.go = spine (Handler, ErrForbidden, query helper, glue postera).
internal/zep/               — adapter Zep: implement ports di memory/, terjemahkan error not-found Zep ke sentinel memory. Satu-satunya yang import SDK Zep.
```

Catatan idiom: `memory/` sengaja public (di luar `internal/`) supaya `internal/zep` bisa
mengimplementasikannya tanpa ada satu package internal yang mengimport package internal lain.
`internal/memory` dan `internal/zep` sama-sama hanya bergantung pada `memory/`, bukan satu sama lain.

## Domain

**agent** — reverse proxy ke ava dan zee. Propagate `user-id`, `time-zone`, `session-id` ke upstream via header. Zee pakai OIDC transport (GCP idtoken), ava tidak.

**memory** — satu `Handler` (`internal/memory`) memfront tiga anggota keluarga memory, lewat port provider-agnostic yang di-back oleh Zep (`internal/zep`):

- *episodic* — sessions, lewat `SessionService`. Ada ownership check manual (`Get` thread → bandingkan `UserID` → baru `Delete`) karena `sessionID` dari URL bisa milik siapa saja.
- *semantic* — knowledge graph, lewat `KnowledgeService`. Tidak butuh ownership check karena operasi sudah di-scope ke `userID` dari JWT (`GetByUserID`, `User.Delete`).
- *prospective* — postera, langsung pakai `*postera.Postarius` (package eksternal). Tidak ada service lokal: Postarius sendiri orchestrator yang scope-aware via context, jadi auth gate cukup di handler.

Endpoints (semua DELETE balas `204 No Content`):

- `/sessions/{id}/messages` — GET/DELETE pesan satu thread.
- `/memory` — GET/DELETE knowledge graph. **DELETE `/memory` memanggil `User.Delete` di Zep yang menghapus seluruh data user termasuk semua threads/sessions — disengaja.**
- `/postera` — GET upcoming, `/postera/{posterum-id}` DELETE cancel.

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
  konsep (mis. `session.go`, `knowledge.go`); package yang punya handler+service diorganisir vertical
  slice — tiap file = satu fitur lengkap dengan handler & service-nya.
- Doc comment package taruh di file fitur utama (mis. `session.go`), bukan `doc.go` terpisah. Deklarasi
  lintas-fitur (sentinel authz, helper bersama, glue tanpa service) taruh di file spine (mis. `handler.go`).
- Sentinel error per fitur, bukan satu yang dipakai bersama (mis. `ErrSessionNotFound`,
  `ErrKnowledgeNotFound`). Adapter terjemahkan error backend ke sentinel; consumer cocokkan via `errors.Is`.
