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
internal/identity/          — Firebase autentikasi + payment guard middleware
internal/link/              — user connect/disconnect akun eksternal. SATU SUBPACKAGE PER INTEGRASI;
                              root hanya berisi shared code yang DITEMUKAN identik lintas integrasi,
                              bukan diramal: state.go (OAuth state HMAC: SignState/VerifyState/StateTTL,
                              dipakai gworkspace & spotify).
internal/knowledge/         — memory semantik: knowledge graph user. service.go = port (Store,
                              consumer-defined) + tipe domain + sentinel (ErrNotFound/ErrForbidden)
                              + Service; handler.go = HTTP glue (Handler: HandleGet, HandleDelete).
internal/knowledge/zep/     — adapter Zep: implement knowledge.Store, terjemahkan not-found Zep ke
                              sentinel knowledge.
internal/link/gworkspace    — linking Google Workspace: handler.go (Handler: HandleAuthURL, HandleConnect,
                              HandleDisconnect).
internal/link/spotify       — linking Spotify: handler.go, struktur sama persis dengan gworkspace
                              (konsumen tokennya yori).
internal/postera/           — memory prospektif: handler.go = HTTP glue di atas postera.Postarius
                              eksternal (Handler: HandleListUpcoming, HandleCancel). Tanpa service/
                              port — Postarius sendiri orchestrator yang scope-aware via context.
internal/session/           — memory episodik: sessions + messages. service.go = port (Store,
                              consumer-defined) + tipe domain + sentinel (ErrNotFound/ErrForbidden)
                              + Service (ownership check); handler.go = HTTP glue (Handler:
                              HandleGetMessages, HandleClearMessages).
internal/session/zep/       — adapter Zep: implement session.Store ("thread" Zep = session domain),
                              terjemahkan not-found Zep ke sentinel session. Sisi agent punya jalur
                              Zep-nya sendiri: adkzep.
internal/wallet/            — fitur wallet FIRST-PARTY, satu vertical slice: ledger.go (port Ledger
                              DOUBLE-ENTRY: Transact = postings balanced jumlah nol, tipe Posting/
                              Spec/Transaction/Entry, Kind open-set, sentinel ErrDuplicateRef,
                              micro-rupiah int64 credit-positive, helper UserAccountID), biller.go
                              (Usage/Price/Receipt + Biller.Charge — token usage → debit user +
                              credit revenue), guard.go (RequireBalance middleware → 402), handler.go
                              (GET /wallet, GET /wallet/usage/today). Lihat WALLET.md untuk keputusan
                              desain + kontrak front end.
internal/wallet/midtrans/   — top-up via Midtrans Snap, satu subpackage per integrasi (pola link):
                              handler.go (HandleCreateTopup = POST /wallet/topup buat transaksi Snap,
                              balas token + redirect URL, TANPA tulisan ledger; HandleNotification =
                              webhook pembayaran, di luar auth Firebase, diautentikasi signature
                              SHA-512 LALU dikonfirmasi ke status API Core Midtrans sebelum
                              membukukan — amount/status/currency dari status API, bukan body
                              (tahan bocornya server key) → Transact topup credit user debit pending,
                              Ref = order ID, idempoten via ErrDuplicateRef → 200). Order ID
                              `topup~{uid}~{nonce}` membawa binding user stateless.
internal/wallet/postgres/   — adapter pgx di database KHUSUS wallet (tabel tanpa prefix): accounts
                              (saldo termaterialisasi, row lock, lock order deterministik by account
                              ID) + transactions (journal header: kind/ref/metadata) + entries
                              (journal lines, append-only). Skema di migrations/ (format goose),
                              dijalankan goose di pipeline deploy — runtime hanya VALIDASI tabel ada,
                              tidak pernah DDL.
```

Catatan idiom: semua package app tinggal di `internal/` — kriterianya LIBRARY vs FIRST-PARTY, bukan
rapi-tidaknya kontrak. Memory dulu package public di root (pola `image` + `image/png`) tapi
diinternalkan karena tidak ada konsumen eksternal: package public di root module aplikasi adalah
komitmen API yang tidak dibutuhkan siapa pun (sharing lintas produk butuh module terpisah apa pun
yang terjadi); lalu package `memory` gabungan dipecah jadi `session`/`knowledge`/`postera` karena
tiga slice itu tidak berbagi apa pun kecuali kata "memory" — package per konsep, bukan per tema.
Pola kontrak+adapter-nya seragam: package fitur berisi port + tipe + sentinel + slice-nya (wallet:
`ledger.go`; session/knowledge: `service.go`), adapter subpackage di sampingnya import package
fitur dan HANYA di-wire di main — service tidak pernah import adapter (consumer-defined interface).
Jangan setengah-setengah: kontrak internal dengan adapter public itu kontradiksi (API public yang
bertipe internal tidak bisa dipakai siapa pun). Aturan penempatan package: package tinggal di
samping hal yang MENDEFINISIKAN TUJUANNYA — adapter zep di samping kontrak session/knowledge yang
dia implement; adapter postgres di samping kontrak wallet; handler linking gworkspace di bawah
`internal/link` karena tujuannya fitur linking app ini.

## Domain

**agent** — group chat in-process. Semua agent menjalankan runner-nya sendiri (`runner.Runner` dari ADK) di atas SATU Zep thread bersama (keyed by `session-id`), jadi human + semua agent baca/tulis satu percakapan.

Instruksi disusun empat lapis. Lapis pertama (base) di-`AdditionalInstruction`-kan ke tiap agent; tiga sisanya di-resolve dari session state via ADK state delta lewat placeholder `{key}` yang dirakit `agent.Instruction()`. Urutan di template: base → kind → run → session.
- `internal/agent/instruction.txt` (base) — dasar bersama SEMUA agent (identitas grup chat, relationship model). Di-embed di `instruction.go`, dirakit `agent.Instruction()` (base + placeholder tiga delta key), dioper sebagai `AdditionalInstruction` ke `ava.New`, `zee.New`, `rafal.New`, dan `yori.New`. Isinya HANYA yang valid untuk kedua jenis agent — behavior spesifik pindah ke lapis kind.
- `KindSpecificInstructionDeltaKey` (`kind_specific_instruction`) — lapis per-JENIS-agent, di-inject via `runner.WithStateDelta` tiap `runner.Run` (bukan dari session service). Ava: `internal/agent/ava/instruction.txt` (orkestrasi — delegasi cukup tag `@nama` manual tanpa mengulang perintah human; balasan final KOSONG kalau specialist sudah menjawab jelas). Specialist: `internal/agent/specialist/instruction.txt` (baca pesan terakhir saat dipanggil dengan tag saja, bertindak, tanpa parroting). Karena `ava` package tidak bisa `//go:embed` file di luar direktorinya, `specialist.KindInstruction` di-export dan dioper ke `ava.NewSubAgent` di main (subagent menjalankan specialist, jadi menyemai kind SPECIALIST). Kind harus di-set di SETIAP run path — placeholder-nya non-optional.
- `SessionInstructionDeltaKey` (`sess_instruction`) — ditulis oleh `adkzep.SessionService` per-session (time awareness, message format; termasuk aturan "jangan echo prefix `[waktu nama]`" — pengetatannya milik adk/zep, bukan di-duplikasi di sini).
- `RunInstructionDeltaKey` (`run_instruction`) — framing per-run, di-inject via `runner.WithStateDelta` tiap `runner.Run`.

Empat pintu masuk ke thread:

- `AvaHandler.HandleHuman` — human ke Ava. Speaker `human`. Tanpa framing per-run (Ava punya behavior group-chat-nya sendiri di module-nya).
- `AvaHandler.HandleSelfAwaken` — Ava dibangunkan postera note-nya sendiri (Cloud Tasks callback, body = raw text). Speaker `ava`, framing `specialist-ran-by-postera-instruction.txt`.
- `SpecialistHandler.HandleHuman` — human ke specialist langsung (mis. `POST /zee`, `POST /rafal`, `POST /yori`). Speaker `human`, framing `specialist-ran-by-human-instruction.txt`.
- `avaSubAgent.Run` — Ava delegasi ke specialist di dalam run-nya sendiri. Speaker `ava`, framing `specialist-ran-by-ava-instruction.txt`.

Ava pemilik self-recall (postera tools), specialist tidak. `ForAva` mengadaptasi specialist menjadi `ava.SubAgent` — adapter hidup di `ava_subagent.go` karena implementasinya milik sisi konsumen (Ava). Route eksplisit per agent — bukan `/{agent}` dispatch.

Iterator `runner.Run` menghasilkan `iter.Seq2[*session.Event, error]`. Consumer wajib drain seluruh iterator. Hanya ambil teks dari `event.IsFinalResponse() && event.Content != nil` — ini selalu event terakhir untuk arsitektur single-agent/tool-based kita. Kalau loop selesai tanpa final response, balas error `502` (atau return error untuk `avaSubAgent.Run`). Error di iterator adalah error infrastruktur — tool call error dikembalikan sebagai FunctionResponse semantic, bukan Go error.

**memory** — tiga package terpisah, satu per anggota keluarga memory, masing-masing vertical slice lengkap dengan handler-nya sendiri (port provider-agnostic di-back oleh Zep di subpackage `zep` masing-masing):

- *episodic* — `internal/session`. Ada ownership check manual di `Service` (`Get` thread → bandingkan `UserID` → baru `Delete`) karena `sessionID` dari URL bisa milik siapa saja.
- *semantic* — `internal/knowledge`. Tidak butuh ownership check karena operasi sudah di-scope ke `userID` dari JWT (`GetByUserID`, `User.Delete`).
- *prospective* — `internal/postera`, langsung pakai `*postera.Postarius` (package eksternal). Tidak ada service lokal: Postarius sendiri orchestrator yang scope-aware via context, jadi auth gate cukup di handler.

Endpoints (semua DELETE balas `204 No Content`):

- `/sessions/messages` — GET/DELETE pesan thread user. Single-session per user: thread = `chat-{userID}` diturunkan server-side dari JWT (`agent.ChatSessionID`), jadi TANPA param sessionID di path.
- `/knowledge` — GET/DELETE knowledge graph. **DELETE `/knowledge` memanggil `User.Delete` di Zep yang menghapus seluruh data user termasuk semua threads/sessions — disengaja.**
- `/postera` — GET upcoming, `/postera/{posterum-id}` DELETE cancel.

**identity** — `FirebaseAuthenticator` middleware verifikasi Firebase ID token via Admin SDK (`auth.Client.VerifyIDToken`), ambil UID, simpan ke context via `user.ContextWithID`. `PaymentGuard` cek Redis set `users:blocked:payment`. Identity adalah concern AVAGENC-LEVEL (platform), bukan chat-level: satu akun Firebase (project `avagenc`) berlaku untuk semua produk Avagenc, sekarang dan nanti.

**wallet** — ledger double-entry rupiah per akun (`user:{uid}` + system `revenue`/`pending`), dipotong per agent run sesuai token usage (WALLET.md = sumber keputusan desain + kontrak front end). Post-paid: `internal/wallet/biller.go` mengakumulasi `event.UsageMetadata` di tiap drain loop (`usage.Add(event)` sebelum branch final response) lalu `Charge` sekali per run via defer — satu transaksi `agent_run` (debit user + credit revenue, SUM postings = 0) dengan metadata `Receipt` (agent/session/trigger/model/breakdown token/snapshot tarif) di header transaksi sekaligus jadi usage log; biller sepackage dengan kontrak + endpoint usage supaya penulis dan pembaca `Receipt` tidak bisa drift (dan supaya tidak ada cycle `agent` ↔ `wallet`). Tarif `wallet.Price` (rupiah per juta token) di-inject eksplisit di main. Gate `RequireBalance` (saldo > 0, habis → 402) di `/ava`, `/zee`, `/rafal`, `/yori`, `/ava/awaken`; debit boleh membuat saldo sedikit minus. Charge gagal = log, bukan 5xx; pakai `context.WithoutCancel`. Migrasi skema: goose di step `Migrate Wallet Database` (deploy.yaml, secret `WALLET_DB_URL`) sebelum deploy; boot hanya validasi. Top-up via Midtrans Snap di `internal/wallet/midtrans` (lihat Struktur + WALLET.md "Top-up Midtrans"); dev pakai Midtrans sandbox via `MIDTRANS_BASE_URL`.

**linking** — surface user-facing untuk connect akun eksternal, sengaja lepas dari agent (agent hanya KONSUMEN token via client yang sama — rafal via `*gworkspace.Client`, yori via `*spotify.Client`; linking yang mengelola grant-nya). Seperti identity, linking adalah concern AVAGENC-LEVEL, bukan chat-level: user connect SEKALI dan grant-nya berlaku untuk semua produk Avagenc (kalau nanti ada produk lain, tidak perlu linking ulang). Karena itu data linking (Firestore di project `avagenc`, database via `FIRESTORE_DATABASE_ID`) di-scope dan dinamai level platform — JANGAN dinamai per-product (bukan `avagenc-chat`). Dua integrasi dengan flow identik (lihat LINKING.md untuk kontrak front end), contoh Google Workspace:

- `GET /gworkspace/auth-url` — mint consent URL Google. State = `integration.exp.HMAC(integration|userID|exp)` (SATU secret bersama `OAUTH_STATE_SECRET` untuk semua integrasi — nama integrasi di mac men-domain-separate-nya; TTL 15 menit, helper di root `internal/link`) — stateless, mengikat flow ke user peminta dan integrasinya.
- `POST /gworkspace/connection` — body `{code, state}` dari callback page front end. Verifikasi state → `Connect` (tukar code, simpan refresh token di Firestore). `ErrMissingScopes`/code ditolak Google → 400; sukses → 204.
- `DELETE /gworkspace/connection` — `Disconnect` (hapus refresh token). Belum connect (`ErrNotConnected`) → 404; sukses → 204. Grant di Google Account user TIDAK di-revoke.

Spotify sama persis dengan prefix `/spotify` (token di Firestore `spotify_tokens`).

Tiap provider me-redirect browser ke halaman callback FRONT END per-integrasi `WEB_APP_URL/{integration}/link/callback` (bukan ke API) — integrasi diketahui dari route, `state` tetap opaque di FE; backend menurunkan redirect URI tiap integrasi dari `WEB_APP_URL`. Semua endpoint linking tetap di belakang auth Firebase.

## Auth flow

Route user di bawah group middleware `firebaseAuthenticator.Authenticate`. User ID tersedia di context via `apiuser.IDFromContext`. Pengecualian: `/ava/awaken` (callback Cloud Tasks) di luar group Firebase — TAPI TETAP diautentikasi: `identity.CloudTasksAuthenticator` memverifikasi OIDC token Google yang dipasang Cloud Tasks (audience = URL target `/ava/awaken`, email = `GCP_RUNTIME_SA_EMAIL`) SEBELUM `apiuser.HTTPWithID` membaca header `user-id`. Tanpa verifikasi ini header `user-id` mentah bisa dipakai siapa saja untuk menguras wallet user lain. Session-nya diturunkan dari user (`chat-{userID}`), bukan dari header.

Single-session per user: semua entry point (human → Ava/specialist, Ava → specialist, awaken) memakai thread yang sama `chat-{userID}` via `agent.ChatSessionID`. Handler agent tidak lagi menerima `session-id` dari header/context; Ava menyuntikkannya ke context (`apisession.ContextWithID`) hanya supaya postera bisa men-scope note-nya.

## Environment variables

| Var | Keterangan |
|-----|-----------|
| `FIREBASE_PROJECT_ID` | Project ID Firebase untuk verifikasi ID token |
| `GOOGLE_APPLICATION_CREDENTIALS` | Path service account credentials (Firebase Admin SDK, Cloud Tasks) |
| `ZEP_API_KEY` | API key Zep |
| `GEMINI_API_KEY` | API key model Gemini (LLM roster) |
| `TUYA_ACCESS_ID` / `TUYA_ACCESS_SECRET` / `TUYA_BASE_URL` | Kredensial Tuya cloud (zee) |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | OAuth client Google Workspace (rafal + linking) — refresh token user di-resolve lewat client ini |
| `SPOTIFY_CLIENT_ID` / `SPOTIFY_CLIENT_SECRET` | OAuth app Spotify (yori + linking) — refresh token user di-resolve lewat client ini |
| `WEB_APP_URL` | Origin web app SPA; backend menurunkan redirect URI tiap integrasi (`WEB_APP_URL/{gworkspace,spotify}/link/callback`) darinya — tiap URL wajib terdaftar verbatim di provider-nya (Google Cloud Console, Spotify Developer Dashboard) |
| `CORS_ALLOWED_ORIGINS` | Daftar origin (dipisah koma) yang boleh memanggil API dari browser; wajib memuat origin tempat SPA disajikan |
| `OAUTH_STATE_SECRET` | Secret HMAC penanda-tangan OAuth state — satu untuk semua integrasi linking (domain separation via nama integrasi di mac) |
| `FIRESTORE_DATABASE_ID` | Database ID Firestore — store account Tuya (`tuya_accounts`), token gworkspace (`gworkspace_tokens`) & token spotify (`spotify_tokens`) |
| `POSTERA_DB_URL` | PostgreSQL connection string untuk postera |
| `WALLET_DB_URL` | PostgreSQL connection string untuk wallet — database KHUSUS wallet (tabel tanpa prefix), terpisah dari postera |
| `MIDTRANS_SERVER_KEY` | Server key Midtrans — auth Snap API (create transaction) + verifikasi signature webhook top-up |
| `MIDTRANS_BASE_URL` | Host Midtrans: `https://app.sandbox.midtrans.com` (dev) / `https://app.midtrans.com` (production) |
| `GCP_PROJECT_ID` | GCP project ID (Cloud Tasks, Firestore) |
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
