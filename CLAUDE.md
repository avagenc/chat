# platform

Platform Avagenc Chat. Menerima request dari client, autentikasi via JWT, lalu menjalankan roster agent (Ava + specialist) **in-process** di atas satu Zep thread bersama, serta menangani domain memory sendiri.

## Struktur

```
cmd/http/main.go            — entrypoint, wire-up EKSPLISIT semua dependency (lihat Conventions → Komposisi)
internal/agent/             — group chat in-process: satu runner per agent di atas Zep thread bersama.
                              instruction.go = konstanta delta key + embed base-instruction.txt + func Instruction()
                              ava_handler.go = AvaHandler (HandleHuman, HandleSelfAwaken)
                              ava_subagent.go = avaSubAgent + ForAva (adapter specialist → ava.SubAgent)
                              specialist_handler.go = SpecialistHandler (HandleHuman)
internal/identity/          — JWT autentikasi + payment guard middleware
memory/                     — package PUBLIC: ports (SessionStore, KnowledgeStore) + tipe domain + sentinel per fitur
                              (ErrSessionNotFound, ErrKnowledgeNotFound). Tidak import apa pun yang internal.
internal/memory/            — HTTP handler + service yang mendorong ports. Vertical slice per fitur:
                              session.go & knowledge.go (handler + service masing-masing);
                              handler.go = spine (Handler, ErrForbidden, query helper, glue postera).
internal/zep/               — adapter Zep: implement ports di memory/, terjemahkan error not-found Zep ke sentinel memory.
                              Satu-satunya yang import SDK Zep.
```

Catatan idiom: `memory/` sengaja public (di luar `internal/`) supaya `internal/zep` bisa
mengimplementasikannya tanpa ada satu package internal yang mengimport package internal lain.
`internal/memory` dan `internal/zep` sama-sama hanya bergantung pada `memory/`, bukan satu sama lain.

## Domain

**agent** — group chat in-process. Semua agent menjalankan runner-nya sendiri (`runner.Runner` dari ADK) di atas SATU Zep thread bersama (keyed by `session-id`), jadi human + semua agent baca/tulis satu percakapan.

Instruksi disusun tiga lapis via ADK state delta:
- `base-instruction.txt` — dasar bersama semua agent, di-embed di `instruction.go`, dipakai sebagai `AdditionalInstruction` ke `ava.New` dan `zee.New`.
- `SessionInstructionDeltaKey` (`temp:sess_instruction`) — ditulis oleh `adkzep.SessionService` per-session (time awareness, message format).
- `RunInstructionDeltaKey` (`temp:run_instruction`) — framing per-run, di-inject via `runner.WithStateDelta` tiap `runner.Run`.

Empat pintu masuk ke thread:

- `AvaHandler.HandleHuman` — human ke Ava. Speaker `human`. Tanpa framing per-run (Ava punya behavior group-chat-nya sendiri di module-nya).
- `AvaHandler.HandleSelfAwaken` — Ava dibangunkan postera note-nya sendiri (Cloud Tasks callback, body = raw text). Speaker `ava`, framing `specialist-ran-by-postera-instruction.txt`.
- `SpecialistHandler.HandleHuman` — human ke specialist langsung (mis. `POST /zee`). Speaker `human`, framing `specialist-ran-by-human-instruction.txt`.
- `avaSubAgent.Run` — Ava delegasi ke specialist di dalam run-nya sendiri. Speaker `ava`, framing `specialist-ran-by-ava-instruction.txt`.

Ava pemilik self-recall (postera tools), specialist tidak. `ForAva` mengadaptasi specialist menjadi `ava.SubAgent` — adapter hidup di `ava_subagent.go` karena implementasinya milik sisi konsumen (Ava). Route eksplisit per agent — bukan `/{agent}` dispatch.

Iterator `runner.Run` menghasilkan `iter.Seq2[*session.Event, error]`. Consumer wajib drain seluruh iterator. Hanya ambil teks dari `event.IsFinalResponse() && event.Content != nil` — ini selalu event terakhir untuk arsitektur single-agent/tool-based kita. Kalau loop selesai tanpa final response, balas error `502` (atau return error untuk `avaSubAgent.Run`). Error di iterator adalah error infrastruktur — tool call error dikembalikan sebagai FunctionResponse semantic, bukan Go error.

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
| `GEMINI_API_KEY` | API key model Gemini (LLM roster) |
| `TUYA_ACCESS_ID` / `TUYA_ACCESS_SECRET` / `TUYA_BASE_URL` | Kredensial Tuya cloud (zee) |
| `ZEE_DB_URL` | PostgreSQL untuk account store Tuya (zee) |
| `POSTERA_DB_URL` | PostgreSQL connection string untuk postera |
| `GCP_PROJECT_ID` | GCP project ID |
| `CLOUD_TASKS_LOCATION_ID` | Cloud Tasks location |
| `CLOUD_TASKS_QUEUE_ID` | Cloud Tasks queue ID |
| `APP_ENV` | `production` atau `development` |
| `PORT` | Port server (default `8080`) |

## Conventions

> **Prinsip utama: clear over clever, explicit over implicit.** Pattern lama package `agent`
> dibongkar justru karena melanggar ini (factory `Build()` opaque, hidden wiring, map dispatch,
> nama generik). JANGAN ulangi pola itu. Aturan di bawah mengikat.

### Komposisi & wiring

- **Wiring EKSPLISIT di consumer (`cmd/.../main.go`), seragam untuk semua fitur.** Dependency dibuat satu per satu di main dan dioper ke konstruktor.
- **Package cuma menyediakan konstruktor kecil yang MENERIMA dependency sudah-jadi** (`NewAvaHandler`, `NewSpecialistHandler`, `ForAva`). DILARANG factory serba-bisa (`Build()`, `Setup()`, `Wire()`) yang bikin dependency-nya sendiri di dalam. Gejala salah: di main cuma `x := pkg.Build(...)` dan seluruh graf dependency lahir tersembunyi di dalam package.
- **Tidak ada hidden DI / hidden wiring.** Semua dependency lewat parameter konstruktor, kelihatan di main.
- **Konstruktor terima interface, bukan tipe konkret**, kalau memungkinkan, supaya package lepas dari adapter.
- **Tidak ada helper/wrapper untuk hal yang sudah simple.** `runner.Run` dipanggil langsung di handler — tidak perlu `respondRun`, `collect`, atau wrapper apapun. Thin = tidak ada lapisan yang tidak menambah nilai.

### File & organisasi

- **Vertical slice per konsep** — satu file = satu konsep lengkap. `ava_handler.go` hanya handler Ava. `ava_subagent.go` hanya adapter sub-agent. `specialist_handler.go` hanya handler specialist. Bukan satu file `ava.go` yang campur aduk.
- **Adapter hidup di sisi konsumen, bukan sisi yang diadaptasi.** `ForAva` ada di `ava_subagent.go` karena `ava.SubAgent` adalah kontrak Ava — specialist tidak tahu soal Ava.
- Tidak ada nama file stutter (`pkg/pkg.go`). Pisah file per konsep; package yang punya handler+service diorganisir vertical slice.
- Doc comment package taruh di file fitur utama, bukan `doc.go` terpisah. Deklarasi lintas-fitur taruh di file spine (mis. `handler.go`, `instruction.go`).

### Endpoint & handler

- **Satu handler per hal konkret, di-route eksplisit** (`/ava`, `/zee`). DILARANG satu handler generik + `map`/`list` yang dispatch lewat path param (`/{agent}`). Nambah anggota = nambah baris wiring eksplisit di main.
- **Nama jujur & spesifik.** Method = aksi sebenarnya (`HandleHuman`, `HandleSelfAwaken`), BUKAN `Chat()`/`Handle()` generik.
- **Teks instruksi/prompt → `//go:embed` file `.txt` terpisah**, satu file per konsep/channel.

### Lain-lain

- **Deklarasi (var, const, type, dsb.) taruh tepat sebelum pertama kali digunakan**, bukan di atas file/fungsi karena terlihat "rapi". Pembaca harus bisa baca dari atas ke bawah tanpa harus scroll ke mana-mana untuk tahu sebuah identifier itu apa.
- Handler thin: hanya HTTP glue (extract param, map error ke status code)
- Service layer hanya kalau ada logika bisnis nyata (ownership check, multi-step orchestration)
- Tidak ada mock testing — integration test untuk behavior, bukan unit test handler
- `go.naturallyfunny.dev/api` menyediakan helper context (user, session, time) dan HTTP utilities
- Sentinel error per fitur, bukan satu yang dipakai bersama. Adapter terjemahkan error backend ke sentinel; consumer cocokkan via `errors.Is`.
