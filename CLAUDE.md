# platform

Platform Avagenc Chat. Menerima request dari client, autentikasi via JWT Firebase, lalu
menjalankan roster agent (Ava + specialist) **in-process** di atas satu Zep thread
bersama, serta menangani domain memory, wallet, dan linking sendiri.

README.md menjelaskan ARSITEKTUR dan REASONING untuk pembaca luar. File ini adalah
**perjanjian kerja**: aturan yang mengikat saat menulis kode di repo ini. Kalau README
dan file ini berbeda soal cara menulis kode, file ini yang menang.

---

# BAGIAN 1 — PRINSIP & LARANGAN (WAJIB DIBACA SEBELUM MENULIS SATU BARIS PUN)

> **Prinsip utama: clear over clever, explicit over implicit, idiomatic over pintar.**
> Pattern lama package `agent` dibongkar habis justru karena melanggar ini: factory
> `Build()` yang opaque, hidden wiring, map dispatch, nama generik. JANGAN PERNAH
> ulangi pola itu dalam bentuk apa pun, sekalipun terlihat "lebih rapi" atau "lebih
> DRY". Aturan di bawah bukan saran — ini mengikat.

## 1.1 Larangan keras (jangan lakukan, tanpa perlu bertanya)

- **DILARANG factory serba-bisa.** Tidak ada `Build()`, `Setup()`, `Wire()`,
  `NewEverything()` yang membuat dependency-nya sendiri di dalam. Gejala salah: di
  `main.go` cuma `x := pkg.Build(cfg)` dan seluruh graf dependency lahir tersembunyi.
- **DILARANG hidden DI / hidden wiring.** Tidak ada container, tidak ada reflection,
  tidak ada code generation untuk dependency. Semua lewat parameter konstruktor,
  kelihatan di `main.go`.
- **DILARANG map/list dispatch lewat path param.** Tidak ada `/{agent}` +
  `map[string]*runner.Runner`. Satu handler per hal konkret, satu route eksplisit.
  Nambah agent = nambah blok wiring eksplisit. Biaya yang kelihatan itu FITUR.
- **DILARANG helper/wrapper untuk hal yang sudah simple.** Tidak ada `respondRun()`,
  `collect()`, `sessionID()`, `mustEnv()`. `runner.Run` dipanggil LANGSUNG di handler.
  Wrapper satu baris di atas concatenation string adalah indireksi tanpa informasi —
  sudah pernah dibuat dan sudah dibongkar (commit `596f7f0`).
  **Uji kelayakan wrapper: kalau tanda tangannya butuh semua variasi call site sebagai
  parameter, dia tidak mengabstraksi apa pun — dia cuma memindahkan kode.**
- **DILARANG abstraksi spekulatif.** Interface dibuat kalau ADA konsumen yang
  membutuhkannya SEKARANG atau ada seam nyata (adapter kedua, test integrasi).
  Interface "untuk jaga-jaga" adalah utang, bukan fleksibilitas.
- **DILARANG service layer kosong.** `Service` ada HANYA kalau ada logika bisnis nyata
  (ownership check, orkestrasi multi-step). `internal/postera` sengaja TIDAK punya
  service karena tidak ada logika untuk ditaruh di sana.
- **DILARANG panic di-recover.** Panic harus crash. Konfigurasi kurang = `log.Fatal`
  saat boot, bukan nil-pointer saat request ke-100. Tidak ada middleware recovery yang
  menyembunyikan bug.
- **DILARANG `float64` di jalur nilai uang.** Semua rupiah adalah `int64` micro-rupiah.
  Tidak ada pengecualian.
- **DILARANG menambah fitur yang tidak diminta.** Kerjakan persis yang diminta. Kalau
  menemukan masalah lain, LAPORKAN, jangan diam-diam diperbaiki sambil lewat.
- **DILARANG menambah dependency tanpa alasan yang tidak bisa dijawab stdlib.** Router
  pihak ketiga sudah dibuang (commit `0a7289b`) karena `net/http.ServeMux` Go 1.22+
  sudah cukup. Setiap dependency baru harus bisa menjawab: "apa yang stdlib tidak bisa?"
- **DILARANG mock testing.** Tidak ada library mock, tidak ada unit test handler yang
  memverifikasi ekspektasi panggilan. Lihat §1.5.

## 1.2 Keharusan (lakukan, selalu)

- **Wiring EKSPLISIT di consumer (`cmd/http/main.go`), seragam untuk semua fitur.**
  Dependency dibuat satu per satu di main dan dioper ke konstruktor. `main.go` adalah
  satu-satunya kebenaran global sistem ini; panjangnya bukan kompleksitas, itu
  keterbukaan.
- **Package cuma menyediakan konstruktor kecil yang MENERIMA dependency sudah-jadi**
  (`ava.NewHandler`, `specialist.NewHandler`, `ava.NewSubAgent`, `agent.NewBiller`).
- **Konstruktor terima interface, bukan tipe konkret**, kalau memungkinkan.
- **Consumer-defined interface.** Port dideklarasikan oleh package yang
  MEMBUTUHKANNYA, seukuran yang dipakai, diimplementasikan adapter di subpackage
  sebelahnya. Package fitur TIDAK PERNAH import adapter-nya sendiri. Ini yang bikin
  graf import bersih dan bisa dicek `go list` — bukan cuma diklaim di dokumen.
- **Urutan middleware adalah properti keamanan** dan harus terbaca dalam satu ekspresi
  di `main.go`. Contoh `/ava/awaken`: API key diverifikasi DULU, baru header `user-id`
  dibaca. Kalau dibalik, siapa pun bisa menguras wallet user lain.
- **Sentinel error per fitur**, bukan satu yang dipakai bersama. Adapter menerjemahkan
  error backend ke sentinel; consumer cocokkan via `errors.Is`. `ErrForbidden`
  di-mint oleh Service, TIDAK PERNAH oleh adapter — storage tidak berhak memutuskan
  otorisasi karena storage tidak punya informasinya.
- **Fail fast saat boot.** Env kosong → `log.Fatal`. Skema DB tidak ada → gagal di
  `NewLedger`, bukan gagal saat charge pertama.
- **Teks instruksi/prompt → `//go:embed` file `.txt` terpisah**, satu file per
  konsep/channel. Tidak ada prompt sebagai string literal di dalam `.go`.

## 1.3 Gaya penulisan Go

- **Deklarasi (var, const, type) taruh TEPAT SEBELUM pertama kali dipakai**, bukan
  ditumpuk di atas file karena terlihat "rapi". Pembaca harus bisa baca dari atas ke
  bawah tanpa scroll ke mana-mana untuk tahu sebuah identifier itu apa. Contoh benar:
  `internal/agent/ava/handler.go` — tiap `//go:embed` channel instruction berdiri tepat
  di atas handler yang memakainya.
- **Vertical slice per konsep — satu file = satu konsep lengkap.** `ava/handler.go`
  hanya handler Ava. `ava/subagent.go` hanya adapter sub-agent. BUKAN satu `ava.go`
  campur aduk.
- **Adapter hidup di sisi KONSUMEN, bukan sisi yang diadaptasi.** `ava.NewSubAgent` ada
  di `internal/agent/ava/` karena `ava.SubAgent` adalah kontrak Ava; specialist tidak
  tahu-menahu soal Ava.
- **Package tinggal di samping hal yang MENDEFINISIKAN TUJUANNYA.** Adapter zep di
  samping kontrak session/knowledge yang dia implement; adapter postgres di samping
  kontrak wallet; handler linking gworkspace di bawah `internal/link` karena tujuannya
  fitur linking app ini.
- **Tidak ada nama file stutter** (`pkg/pkg.go`). Doc comment package taruh di file
  fitur utama, BUKAN `doc.go` terpisah. Deklarasi lintas-fitur taruh di file spine
  (`handler.go`, `instruction.go`).
- **Nama jujur & spesifik.** Method = aksi sebenarnya (`HandleHuman`,
  `HandleSelfAwaken`, `HandleVoice`), BUKAN `Chat()`/`Handle()`/`Process()` generik.
- **Handler thin.** Hanya HTTP glue: extract param, panggil service/runner, map error
  ke status code. Tidak ada logika bisnis di handler.

## 1.4 Comment: WAJIB menjelaskan KENAPA, DILARANG mengulang APA

Ini aturan yang paling sering dilanggar, jadi dieksplisitkan:

- **WAJIB**: doc comment package yang menjelaskan alasan package itu ada dan batasnya;
  comment di atas port/kontrak yang menjelaskan invariant dan konvensi (mis. konvensi
  credit-positive di `wallet/ledger.go`); comment yang menjelaskan keputusan
  non-obvious atau yang akan SALAH DIBACA reviewer (kenapa balasan final kosong itu
  SUKSES, kenapa `context.WithoutCancel` dipakai, kenapa posting di-sort sebelum
  dikunci).
- **DILARANG**: comment yang menarasikan kode. `// increment counter` di atas `i++`.
  `// Handler is a handler.` Kalau comment-nya bisa dihapus tanpa ada informasi yang
  hilang, hapus.
- Direktif fungsional (`//go:embed`) jelas bukan comment biasa dan selalu boleh.

Patokan: **kode menjelaskan APA; comment menjelaskan KENAPA dan APA YANG AKAN SALAH
DIBACA.** Semua comment di repo ini sudah mengikuti pola itu — pertahankan, jangan
tambahkan comment jenis lain.

## 1.5 Testing

- **Tidak ada mock testing.** Test perilaku nyata, bukan ekspektasi terhadap dobel.
- **`wallet` SENGAJA belum punya test, karena masih EKSPERIMENTAL.** Skema jurnalnya
  masih bisa berubah dan **payment gateway-nya belum ditetapkan** — Midtrans sudah
  ditulis lengkap tapi masih di-comment out di `main.go` menunggu keputusan produk,
  dan kalau gateway-nya ganti, alur top-up beserta test-nya ikut dibuang. Menulis
  integration test di atas bentuk yang belum final = menulis test untuk dihapus. Suite
  lamanya (ledger + midtrans) sudah DIHAPUS, bukan dibiarkan SKIP selamanya.
  Konsekuensinya harus disadari, bukan dilupakan: **jalur SQL, migration, constraint
  trigger sum-zero, idempotensi `ref`, urutan row lock, dan webhook Midtrans TIDAK
  diverifikasi apa pun.** Kalau menyentuh salah satunya, verifikasi manual terhadap
  database sekali pakai — jangan mengaku aman karena `go test` hijau. **Begitu skema
  dan gateway ditetapkan, integration test wajib ditulis ulang** — ini penundaan,
  bukan keputusan permanen.
- **Table-driven test untuk logika murni** yang bisa jalan tanpa infrastruktur:
  `internal/link` (HMAC state), `internal/agent` (aritmetika billing),
  `internal/knowledge/zep` (drain pagination).
- **In-memory ledger di `agent/biller_test.go` BUKAN mock.** Dia implementasi NYATA dari
  `agent.Ledger`/`agent.LedgerReader`: MENEGAKKAN aturan balanced-postings (spec tidak
  seimbang ditolak) dan menjawab `Entries`/`Balance` dari yang benar-benar dia simpan,
  tanpa ekspektasi terekam. Kalau ragu bedanya: mock memverifikasi PANGGILAN,
  implementasi kedua memverifikasi PERILAKU. **Ini juga alasan kedua port itu ada** —
  `wallet.Ledger` sekarang struct PostgreSQL konkret, jadi tanpa port di sisi agent,
  aritmetika uang hanya bisa dites kalau ada database.
- **DILARANG unit test handler dengan dobel HTTP.** Handler adalah glue; kegagalannya
  adalah kegagalan wiring, dan itu tidak ditangkap test yang wiring-nya palsu.
- Laporkan hasil test APA ADANYA. Test yang skip disebut skip, bukan disebut lulus.

---

# BAGIAN 2 — STRUKTUR

```
cmd/http/main.go            entrypoint, wire-up EKSPLISIT semua dependency (§1.2)
cmd/httpdev/                KEMBARAN cmd/http KHUSUS DEVELOPMENT: wiring identik, tapi
                            identitas user datang dari header `user-id`
                            (apiuser.HTTPWithID), bukan bearer Firebase, jadi API bisa
                            dipukul pakai curl polos. UNTRACKED (.gitignore) dan JANGAN
                            PERNAH di-deploy — Dockerfile build `./cmd/http` by name.
                            Binary terpisah, BUKAN bypass ber-flag di binary produksi:
                            binary yang tidak ada di image tidak bisa salah konfigurasi.
cmd/wallet/{migrate,seed}/  CLI lokal untuk wallet DB. UNTRACKED juga.

internal/agent/
  instruction.go            TIGA konstanta state-delta key (ChannelInstructionDeltaKey,
                            RunInstructionDeltaKey, SessionInstructionDeltaKey) + var
                            Instruction (embed instruction.txt) = LAPIS DASAR bersama
  instruction.txt           template base: isinya HANYA yang valid untuk KEDUA jenis
                            agent. `%s` diisi tiap kind saat build; placeholder
                            {channel_instruction}/{run_instruction}/{sess_instruction}
                            dibiarkan utuh untuk di-resolve per run.
  biller.go                 Usage/Price/Run/Receipt + TxAgentRun (untyped) + port
                            Ledger (Transact saja) + Biller.Charge — token usage →
                            satu transaksi agent_run (debit user + credit revenue). Di
                            sini, bukan di wallet: harga model itu urusan produk ini.
                            Port-nya BUKAN untuk menghindari import (agent memang
                            import wallet, dan itu sah): dia ada karena punya DUA
                            implementasi nyata hari ini — *wallet.Ledger di produksi,
                            ledger in-memory di test — sehingga aritmetika uang tetap
                            teruji tanpa PostgreSQL. Ini paruh kedua dari "accept
                            interfaces, return structs"; jangan dihapus.
  usage.go                  port LedgerReader (Entries saja) + UsageHandler.HandleToday
                            (GET /wallet/usage/today) — pembaca Receipt, sepackage
                            dengan penulisnya supaya bentuk JSON-nya tidak bisa drift.
                            Port terpisah dari biller.go: satu file satu kebutuhan,
                            sehingga endpoint yang cuma melaporkan tidak bisa
                            membukukan.
  biller_test.go            aritmetika billing + bentuk posting (pasangan balanced,
                            akun benar, type) terhadap ledger in-memory yang memenuhi
                            kedua port di atas (§1.5)
  ava/handler.go            Handler Ava: HandleHuman (teks), HandleVoice, HandleSelfAwaken
  ava/subagent.go           subAgent + NewSubAgent — adapter specialist → ava.SubAgent
  ava/instruction.go        Instruction() = base + kind Ava
  ava/*.txt                 instruction.txt (kind), ran-in-text-channel-instruction.txt,
                            ran-in-voice-channel-instruction.txt,
                            ran-by-postera-instruction.txt
  specialist/handler.go     Handler specialist (dipakai zee, rafal, yori): HandleHuman
  specialist/instruction.go Instruction() = base + kind Specialist
  specialist/*.txt          instruction.txt (kind), ran-by-human-instruction.txt,
                            ran-by-ava-instruction.txt (di-export sebagai
                            RanByAvaInstruction, dioper ke ava.NewSubAgent di main)

internal/identity/
  firebase.go               FirebaseAuthenticator — verifikasi ID token via Admin SDK →
                            apiuser.ContextWithID
  apikey.go                 APIKeyAuthenticator — constant-time compare header `api-key`.
                            Dipakai DUA KALI dengan secret berbeda: POSTERA_API_KEY
                            untuk /ava/awaken, THIRD_PARTY_API_KEY untuk /ava/voice.

internal/link/              user connect/disconnect akun eksternal. SATU SUBPACKAGE PER
                            INTEGRASI; root hanya berisi shared code yang DITEMUKAN
                            identik lintas integrasi, bukan diramal.
  state.go                  OAuth state HMAC: SignState/VerifyState/StateTTL
  state_test.go             table-driven (CSRF, cross-integration replay, expiry, tamper)
  gworkspace/handler.go     HandleAuthURL, HandleStatus, HandleConnect, HandleDisconnect
  spotify/handler.go        struktur sama persis dengan gworkspace
  tuya/handler.go           HANYA HandleStatus — Tuya tidak punya OAuth publik di sini,
                            akun di-link manual oleh tim (VIP onboarding)

internal/knowledge/         memory SEMANTIK: knowledge graph user
  service.go                port Store (consumer-defined) + tipe domain + sentinel
                            (ErrNotFound/ErrForbidden) + Service. Port TANPA pagination:
                            graf cuma bermakna kalau utuh (edge tanpa kedua ujungnya
                            tidak bisa digambar), jadi Store mengembalikan graf UTUH.
  handler.go                HandleGet, HandleDelete
  zep/store.go              adapter Zep. Zep membatasi tiap halaman 50 item, jadi
                            adapter yang MENGURAS halaman (drain, cursor = UUID item
                            terakhir) — pagination adalah detail backend, tidak bocor ke
                            port maupun front end.
  zep/store_test.go         test drain: multi-halaman, kelipatan pas, backend yang
                            mengabaikan cursor, page cap, error di tengah

internal/session/           memory EPISODIK: sessions + messages
  service.go                port Store + tipe domain + sentinel + Service (ownership
                            check manual: Get thread → bandingkan UserID → baru Delete)
  handler.go                HandleGetMessages, HandleClearMessages
  zep/store.go              adapter Zep ("thread" Zep = session domain)

internal/postera/handler.go memory PROSPEKTIF: HTTP glue di atas postera.Postarius
                            eksternal (HandleListUpcoming, HandleCancel). TANPA
                            service/port — Postarius sendiri orchestrator yang
                            scope-aware via context.

wallet/                     DI ROOT, BUKAN internal/ — satu-satunya package aplikasi
                            yang begitu, dan itu disengaja: wallet memang diniatkan
                            dipakai lintas produk Avagenc, sementara sisanya tidak.
                            TIDAK TAHU apa yang dijual konsumennya: tidak ada
                            Usage/Price/Receipt di sini, tidak ada transaction type
                            yang dia tafsirkan sendiri. Karena dia library dan bukan fitur,
                            `internal/agent` boleh meng-import-nya tanpa melanggar
                            aturan graf import (lihat BAGIAN 6 §4).
  ledger.go                 tipe Posting/Spec/Transaction/Entry/EntriesQuery,
                            sentinel ErrDuplicateRef, micros int64 credit-positive,
                            Currency (+ IDR), ID akun ber-currency
                            (UserAccountID/ParseUserAccountID/RevenueAccountID/
                            PendingAccountID). `Spec.Type`/`Entry.Type` adalah
                            `string` POLOS, bukan tipe bernama — himpunannya terbuka
                            dan ledger tidak pernah membacanya, jadi konsumen yang
                            mendeklarasikan konstanta untyped-nya sendiri
                            (agent.TxAgentRun, midtrans.TxTopup), pola net/http
                            MethodGet. JANGAN dijadikan tipe bernama lagi: itu
                            mengklaim kosakata milik pemakai, dan kolom SQL-nya
                            tetap bernama `kind` — Go-nya saja yang diganti supaya
                            tidak bentrok dengan lapis KIND instruksi di
                            internal/agent — DAN `Ledger` itu sendiri: STRUCT pgx
                            konkret, BUKAN interface. Tidak ada port di sini dan
                            jangan ditambahkan: jaminan yang bikin ini ledger hidup
                            di Postgres, bukan di Go (constraint trigger deferred
                            SUM=0, unique partial index di ref, row lock berurutan
                            account ID), jadi interface hanya akan menjanjikan
                            substitutability yang cuma bisa dipenuhi sesuatu yang
                            lebih lemah. Konsumen yang butuh seam mendeklarasikan
                            interface-nya SENDIRI di sisinya (lihat agent/biller.go).
                            Database KHUSUS wallet, tabel tanpa prefix: accounts
                            (identitas + currency + row lock — TANPA kolom saldo) +
                            transactions (journal header) + entries (journal lines,
                            append-only; saldo = SUM(amount) dari sini).
  migrations.go             embed migrations/*.sql (dipakai test + cmd/wallet/migrate)
  migrations/               skema goose, TANPA seksi Down (test menjalankan file apa
                            adanya). Dijalankan goose di pipeline deploy dengan
                            `-dir wallet/migrations` — runtime hanya VALIDASI tabel
                            ada, tidak pernah DDL.
  guard.go                  Guard.RequireBalance middleware → 402. Tetap di sini, BUKAN
                            di agent: dia cuma butuh Balance dan tidak kenal agent.
  handler.go                HandleBalance (GET /wallet)
  midtrans/                 top-up via Midtrans Snap, satu subpackage per integrasi
                            (pola link). LENGKAP tapi TANPA TEST dan SAAT INI DI-COMMENT
                            OUT di main.go menunggu keputusan produk.
```

Lihat README.md untuk arsitektur menyeluruh.

**Catatan idiom penempatan package.** Kriterianya SATU: **apakah package ini memang
diniatkan dipakai lintas produk?** Kalau ya, dia di root. Kalau tidak, `internal/`.
Bukan soal rapi-tidaknya kontrak, bukan soal seberapa bersih port-nya.

`wallet` ada di ROOT karena itu memang niatnya sejak awal: buku besar yang tidak tahu
apa yang dijual konsumennya, dimaksudkan melayani produk Avagenc mana pun. Konsekuensi
langsungnya: `internal/agent` boleh meng-import-nya tanpa melanggar apa pun, karena itu
bukan fitur mengintip fitur tetangga — itu aplikasi memakai library-nya sendiri.
Sebaliknya memory dulu juga package public di root (pola `image` + `image/png`) tapi
DIINTERNALKAN, karena tidak pernah ada niat menggeneralkannya lintas project — dan
package public di root tanpa niat itu cuma komitmen API yang tidak dibutuhkan siapa pun.

Lalu package `memory` gabungan dipecah jadi `session`/`knowledge`/`postera` karena tiga
slice itu tidak berbagi apa pun kecuali kata "memory" — **package per konsep, bukan per
tema.** Pola kontrak+adapter berlaku di mana backend-nya memang bisa ditukar:
`session`/`knowledge` mendeklarasikan port `Store` di `service.go`, adapter Zep di
subpackage sebelahnya, di-wire HANYA di main — service TIDAK PERNAH import adapter.
**`wallet` sengaja TIDAK mengikuti pola itu**: dia bukan kontrak di atas backend yang
bisa ditukar, dia PostgreSQL, dan pura-pura sebaliknya justru menyesatkan (§BAGIAN 3
§wallet). Jangan "seragamkan" dengan menambah port di sana.

---

# BAGIAN 3 — DOMAIN

## agent

Group chat in-process. Semua agent menjalankan runner-nya sendiri (`runner.Runner` dari
ADK) di atas SATU Zep thread bersama (`chat-{userID}`), jadi human + semua agent
baca/tulis satu percakapan.

### Empat lapis instruksi

Lapis BASE adalah `agent.Instruction` — di-embed di package `agent` yang MEMANG SUDAH
mendeklarasikan tiga delta key dan MEMANG SUDAH di-import `ava` maupun `specialist`,
sementara `agent` sendiri tidak meng-import keduanya. Jadi tidak ada package tambahan
dan tidak ada import cycle.

Lapis KIND dirakit saat BUILD (`fmt.Sprintf` di `ava.Instruction()` /
`specialist.Instruction()` mengisi `%s` di template base), lalu dioper sebagai
`AdditionalInstruction` ke `ava.New`, `zee.New`, `rafal.New`, `yori.New`. Tiga lapis
lain di-resolve per-run dari session state via placeholder `{key}`.

Urutan render di `internal/agent/instruction.txt`:

```
[WHERE_AM_I] + [RELATIONSHIP_MODEL_*]     BASE     · dasar bersama semua agent
{channel_instruction}                     CHANNEL  · teks vs suara (per run)
%s                                        KIND     · diisi saat build
{run_instruction}                         RUN      · siapa yang memicu giliran (per run)
{sess_instruction}                        SESSION  · ditulis adkzep (per session)
```

- **BASE** — identitas grup chat + relationship model. Isinya HANYA yang valid untuk
  KEDUA jenis agent; behavior spesifik pindah ke lapis kind.
- **KIND** — `ava/instruction.txt` (orkestrasi: delegasi = memanggil TOOL, isi pesan
  persis `"@nama"`, tidak mengulang perintah human, tag bukan balasan final) atau
  `specialist/instruction.txt` (dipanggil dengan tag saja = baca pesan terakhir,
  bertindak, tanpa parroting).
- **CHANNEL** (`channel_instruction`) — teks vs suara. Aturan "diam itu benar" ada di
  channel teks; aturan "jangan pernah diam, human sedang menelepon" + format TTS + tag
  `#emosi#` + `#end#` ada di channel suara. Specialist tidak punya lapis channel →
  di-set `""`.
- **RUN** (`run_instruction`) — framing per-run: siapa yang memicu giliran ini.
- **SESSION** (`sess_instruction`) — ditulis `adkzep.SessionService` per-session (time
  awareness, message format, aturan "jangan echo prefix `[waktu nama]`").
  Pengetatannya milik agentkit/zep, TIDAK diduplikasi di sini.

**Placeholder bersifat non-optional: SETIAP run path WAJIB men-set channel DAN run**,
walau nilainya `""`. Key yang tidak di-set meninggalkan literal `{run_instruction}` di
prompt.

### Empat pintu masuk ke thread

| Pintu | Speaker | Channel | Run instruction |
|---|---|---|---|
| `ava.Handler.HandleHuman` (`POST /ava`) | `human` | text | `""` |
| `ava.Handler.HandleVoice` (`POST /ava/voice`) | `human` | voice | `""` |
| `ava.Handler.HandleSelfAwaken` (`POST /ava/awaken`) | `postera` (system role) | text | `ran-by-postera-instruction.txt` |
| `specialist.Handler.HandleHuman` (`POST /zee`,`/rafal`,`/yori`) | `human` | `""` | `ran-by-human-instruction.txt` |
| `subAgent.Run` (Ava delegasi) | `ava` | `""` | `ran-by-ava-instruction.txt` |

Ava pemilik self-recall (postera tools), specialist tidak. `ava.NewSubAgent`
mengadaptasi specialist menjadi `ava.SubAgent`. Route eksplisit per agent — BUKAN
`/{agent}` dispatch.

### Kontrak event stream

Iterator `runner.Run` menghasilkan `iter.Seq2[*session.Event, error]`.

1. Consumer WAJIB drain seluruh iterator.
2. Hanya ambil teks dari `event.IsFinalResponse()`; `event.Content` boleh nil.
3. **Balasan final KOSONG adalah SUKSES**, bukan kegagalan — agent boleh bertindak
   tanpa ada yang perlu dikatakan, dan turn tanpa teks tidak dipersist ke thread.
4. Loop selesai tanpa final response → `502` (atau return error untuk `subAgent.Run`).
5. Error di iterator = error INFRASTRUKTUR. Tool call yang gagal kembali sebagai
   FunctionResponse semantic, BUKAN Go error.
6. `usage.Add(event)` dipanggil untuk SETIAP event sebelum branch final response, dan
   `Charge` dipanggil lewat `defer` — token yang terpakai sebelum error tetap dibayar.

### Timeout

Satu run orkestrasi bisa panjang (Ava + beberapa specialist, tiap giliran satu panggilan
model), jadi timeout di-set eksplisit di `main.go` dan saling terkait:
`http.Server.WriteTimeout` 310s memberi ruang satu run penuh (`ReadHeaderTimeout` 10s /
`ReadTimeout` 30s / `IdleTimeout` 120s), sementara satu panggilan model dibatasi 90s
(`genai.HTTPOptions.Timeout`) supaya provider yang menggantung tidak menyandera seluruh
run. Client yang menunggu lebih lama dari itu akan lihat koneksi diputus — BUKAN bug FE.

Model roster: `gemini-3.5-flash`, di-set eksplisit di `main.go`, bukan env.

## memory

Tiga package terpisah, satu per anggota keluarga memory, masing-masing vertical slice
lengkap dengan handler sendiri (port provider-agnostic di-back oleh Zep di subpackage
`zep` masing-masing):

- **episodik** — `internal/session`. ADA ownership check manual di `Service` karena
  `sessionID` secara struktur bisa milik siapa saja.
- **semantik** — `internal/knowledge`. TIDAK butuh ownership check karena operasi sudah
  di-scope ke `userID` dari JWT (`GetByUserID`, `User.Delete`) — tidak ada ID dari
  request yang bisa dikelabui.
- **prospektif** — `internal/postera`, langsung pakai `*postera.Postarius`. Tidak ada
  service lokal: Postarius sendiri orchestrator yang scope-aware via context, jadi auth
  gate cukup di handler.

Endpoints (semua DELETE balas `204 No Content`):

- `/sessions/messages` — GET/DELETE pesan thread user. Single-session per user: thread =
  `chat-{userID}` diturunkan server-side dari JWT (`"chat-" + userID` inline di tiap
  pemakaian), jadi TANPA param sessionID di path.
- `/knowledge` — GET/DELETE knowledge graph. GET membalas graf UTUH (`{nodes, edges}`) —
  tidak ada param `limit`/`cursor`, adapter Zep yang menguras halamannya.
  **DELETE `/knowledge` memanggil `User.Delete` di Zep yang menghapus SELURUH data user
  termasuk semua threads/sessions — DISENGAJA.**
- `/postera` — GET upcoming, `/postera/{posterumID}` DELETE cancel.

## identity

Tiga jenis pemanggil, tiga mekanisme:

- `FirebaseAuthenticator` — SPA sebagai user yang sign-in. Verifikasi Firebase ID token
  via Admin SDK (`auth.Client.VerifyIDToken`), simpan UID ke context via
  `user.ContextWithID`.
- `APIKeyAuthenticator(POSTERA_API_KEY)` — Cloud Tasks di `/ava/awaken`.
- `APIKeyAuthenticator(THIRD_PARTY_API_KEY)` — klien voice pihak ketiga di `/ava/voice`.
  **SEMENTARA** — lihat catatan di BAGIAN 4.

Identity adalah concern **AVAGENC-LEVEL** (platform), bukan chat-level: satu akun
Firebase (project `avagenc`) berlaku untuk semua produk Avagenc, sekarang dan nanti.

## wallet

Ledger double-entry rupiah per akun (`user:{uid}` + system `revenue`/`pending`),
dipotong per agent run sesuai token usage.

**Billing hidup di `internal/agent`, pembukuan di `wallet` (ROOT), dan garis itu
sengaja.** Menaksir harga sebuah model adalah urusan produk INI — makanya di
`internal/`. Buku besarnya tidak pernah tahu apa yang dibayar dan memang diniatkan
melayani produk Avagenc mana pun — makanya di root, sejajar `cmd/`, bukan di dalam
`internal/`. Karena itu `Usage`/`Price`/`Run`/`Receipt` + `TxAgentRun` +
`Biller.Charge` ada di `agent/biller.go`, dan `wallet` tidak punya satu pun tipe yang
menyebut "run".

Post-paid: `agent/biller.go` mengakumulasi `event.UsageMetadata` di tiap drain loop
lalu `Charge` sekali per run via `defer`. **Tiap file mendeklarasikan port-nya sendiri,
seukuran yang dia pakai**: `Ledger` (`Transact` saja) di `biller.go` karena menagih itu
menulis, dan `LedgerReader` (`Entries` saja) di `usage.go` karena melaporkan itu
membaca — jadi Biller tidak bisa mengintip saldo dan endpoint usage tidak bisa
membukukan, sekalipun keduanya di-wire dengan `*wallet.Ledger` yang sama. Kedua port
itu juga yang bikin aritmetika uang bisa dites tanpa database (§1.5). Biller
merakit sendiri satu transaksi `agent_run` (debit `user:{uid}:IDR` + credit
`revenue:IDR`, SUM postings = 0) dengan metadata `Receipt` (agent/session/trigger/
model/breakdown token/snapshot tarif) sekaligus jadi usage log.

**`internal/agent` meng-import `wallet` secara langsung, dan itu SAH** — bukan fitur
mengintip fitur tetangga, tapi aplikasi memakai library-nya sendiri (§BAGIAN 6 no. 4).
Pernah dicoba menghilangkan import itu dengan port sempit `agent.Ledger` (`RecordRun`/
`RunsSince`) plus adapter di package `internal/agent/wallet` — dan itu DIBATALKAN.
Alasannya: edge-nya tidak hilang, cuma pindah ke package yang justru tidak akan dicari
pembaca, dengan ongkos satu interface bernama `Ledger` yang bukan ledger (isinya nol
data — cuma meneruskan ke ledger wallet yang sama) plus satu lapis pembungkus yang
harus dibaca sebelum orang paham uangnya mendarat di mana. **Jangan ulangi.** Kalau
suatu hari wallet benar-benar pindah ke balik panggilan jaringan, yang berubah adalah
apa yang di-wire di main ke `agent.Ledger`/`agent.LedgerReader` — port itu sudah ada di
sisi yang membutuhkannya, dan `Biller` tidak perlu tahu.

`Biller` sepackage dengan `HandleToday` (`usage.go`) supaya penulis dan pembaca
`Receipt` tidak bisa drift — itulah alasan endpoint `GET /wallet/usage/today` ikut
pindah ke `agent` walau path-nya tetap di bawah `/wallet` (path adalah kontrak FE,
bukan peta package).

Tarif `agent.Price` (rupiah per juta token) di-inject eksplisit di main, bukan env:
snapshot-nya tercatat di tiap transaksi, jadi ubah tarif = keputusan deploy.

Gate `RequireBalance` (saldo > 0, habis → 402) di `/ava`, `/ava/voice`, `/ava/awaken`,
`/zee`, `/rafal`, `/yori`; debit boleh membuat saldo sedikit minus — billing write TIDAK
BOLEH hilang karena dana habis. Charge gagal = log, BUKAN 5xx; pakai
`context.WithoutCancel` supaya debit tertulis walau client disconnect.

Migrasi skema: goose di step `Migrate Wallet Database` (deploy.yaml, secret
`WALLET_DB_URL`) SEBELUM deploy; boot hanya VALIDASI tabel ada, tidak pernah DDL.

Top-up via Midtrans Snap ada lengkap di `wallet/midtrans`: webhook
diautentikasi signature SHA-512 LALU dikonfirmasi ke status API Core Midtrans sebelum
membukukan — amount/status/currency diambil dari status API, BUKAN dari body, supaya
server key yang bocor tidak bisa memalsukan pembukuan. **Saat ini di-comment out di
`main.go` menunggu keputusan produk**; kode + test-nya dipertahankan supaya
mengaktifkannya kembali = uncomment, bukan tulis ulang.

## linking

Surface user-facing untuk connect akun eksternal, SENGAJA lepas dari agent (agent hanya
KONSUMEN token via client yang sama — rafal via `*gworkspace.Client`, yori via
`*spotify.Client`, zee via `*tuya.Client`; linking yang mengelola grant-nya). Seperti
identity, linking adalah concern **AVAGENC-LEVEL**: user connect SEKALI dan grant-nya
berlaku untuk semua produk Avagenc. Karena itu data linking (Firestore di project
`avagenc`, database via `FIRESTORE_DATABASE_ID`) di-scope dan dinamai level platform —
JANGAN dinamai per-product (bukan `avagenc-chat`).

Dua integrasi OAuth dengan flow identik, contoh Google Workspace:

- `GET /gworkspace/auth-url` — mint consent URL. State =
  `integration.exp.HMAC(integration|userID|exp)` — SATU secret bersama
  `OAUTH_STATE_SECRET` untuk semua integrasi (nama integrasi di mac
  men-domain-separate), TTL 15 menit, helper di root `internal/link`. Stateless,
  mengikat flow ke user peminta dan integrasinya.
- `GET /gworkspace/connection` — `{"connected": bool}`. Kebenaran STORAGE saja, tidak
  mem-probe provider.
- `POST /gworkspace/connection` — body `{code, state}` dari callback page FE. Verifikasi
  state → `Connect` (tukar code, simpan refresh token di Firestore).
  `ErrMissingScopes` / code ditolak Google → 400; sukses → 204.
- `DELETE /gworkspace/connection` — `Disconnect` (hapus refresh token). Belum connect
  (`ErrNotConnected`) → 404; sukses → 204. Grant di akun Google user TIDAK di-revoke
  (batasan SDK).

Spotify sama persis dengan prefix `/spotify` (token di Firestore `spotify_tokens`).
Tuya HANYA `GET /tuya/connection` (status) karena akun Tuya di-link manual oleh tim.

Tiap provider me-redirect browser ke halaman callback FRONT END per-integrasi
`WEB_APP_URL/{integration}/link/callback` (BUKAN ke API) — integrasi diketahui dari
route, `state` tetap opaque di FE; backend menurunkan redirect URI tiap integrasi dari
`WEB_APP_URL`. Semua endpoint linking tetap di belakang auth Firebase.

---

# BAGIAN 4 — AUTH FLOW

Route user di bawah middleware `firebaseAuthenticator.Authenticate`. User ID tersedia di
context via `apiuser.IDFromContext`.

Dua pengecualian, keduanya TETAP diautentikasi dengan API key SEBELUM identitas dibaca
dari header:

```go
// POST /ava/awaken — callback Cloud Tasks
posteraAuthenticator.Authenticate(          // 1. buktikan pemanggil = queue kita
    apiuser.HTTPWithID(                     // 2. BARU header `user-id` dipercaya
        walletGuard.RequireBalance(...)))
```

**Urutan ini ADALAH properti keamanannya.** Tanpa verifikasi API key lebih dulu, header
`user-id` mentah bisa dipakai siapa saja untuk menguras wallet user lain. `/ava/voice`
memakai pola yang sama dengan `THIRD_PARTY_API_KEY`.

**`/ava/voice` API-key-only itu SEMENTARA, dan bedanya dengan `/ava/awaken` harus
dipahami sebelum menyentuh route ini.** Cloud Tasks adalah infrastruktur KITA — queue-nya
dikonfigurasi aplikasi ini, jadi membuktikan "pemanggilnya queue kita" setara dengan
membuktikan request-nya sah. Perangkat voice BUKAN infrastruktur kita: shared secret di
sana mengautentikasi KLIEN, bukan USER, jadi siapa pun yang mengekstrak key dari perangkat
bisa mengirim `user-id` siapa saja. Ini afordans pengembangan perangkat (sign-in di
hardware tanpa browser, secure storage, refresh token per jam) yang sengaja ditunda, bukan
desain final.

Arah perubahannya sudah ditentukan, jadi JANGAN merancang ulang: `/ava/voice` pindah ke
bawah `firebaseAuthenticator.Authenticate`, dan **`apiuser.HTTPWithID` beserta header
`user-id` DIHAPUS dari route itu** — identitas datang dari token terverifikasi, karena itu
satu-satunya cara pemanggil berhenti bisa memilih dirinya sendiri. API key boleh tetap ada
di DEPAN sebagai atestasi klien (key dulu, baru bearer), tapi berhenti jadi penentu
identitas. Sampai itu terjadi: jangan menulis kode, komentar, atau dokumen yang
menyiratkan susunan sekarang adalah desain final; kalau ada yang bertanya, jawabannya ada
di README §Trust boundaries.

**JANGAN tambahkan kembali OIDC Cloud Tasks di sini.** Dulu queue di-wire dengan
`posteracloudtasks.WithServiceAccountEmail(...)` sehingga Cloud Tasks memasang token OIDC
Google — tapi tidak ada satu pun kode yang memverifikasinya, dan Cloud Run jalan dengan
`--allow-unauthenticated`, jadi platform juga tidak memverifikasinya. Konfigurasi itu
TERLIHAT seperti kontrol keamanan padahal tidak menegakkan apa pun, jadi sudah dihapus
(beserta env `GCP_RUNTIME_SA_EMAIL` di sisi aplikasi; GitHub variable-nya tetap ada karena
dipakai `--service-account` di deploy). Kalau suatu saat ingin identitas yang
diverifikasi platform, sadari batasannya: **IAM Cloud Run itu per-SERVICE, bukan
per-ROUTE** — service ini juga melayani SPA, jadi mematikan `--allow-unauthenticated`
akan mengunci semua route user. Artinya butuh service terpisah untuk callback, dan itu
keputusan arsitektur, bukan sekadar menambah satu opsi.

Single-session per user: semua entry point (human → Ava/specialist, Ava → specialist,
awaken, voice) memakai thread yang sama `chat-{userID}`. Handler agent tidak menerima
`session-id` dari header/context.

Postera TIDAK di-wire dengan `WithSessionFromContext`: karena single-session-per-user,
`Session` posterum selalu sama dengan `Human`-nya, jadi scoping tambahan itu tidak
pernah menambah pembatasan apa pun di atas scoping `Human` yang sudah ada — redundant,
BUKAN defense-in-depth. Filter redundan yang TERLIHAT seperti kontrol keamanan lebih
buruk daripada tidak ada, karena pembaca berikutnya akan mengira ada batas di situ.

CORS: SATU origin (`CORS_ALLOWED_ORIGIN`), dicocokkan persis, `Vary: Origin` selalu
di-set. Bukan daftar, bukan regex, bukan wildcard. Satu deployment backend = satu origin
web.

---

# BAGIAN 5 — ENVIRONMENT VARIABLES

Semua WAJIB (fatal saat boot kalau kosong) kecuali yang ditandai.

| Var | Keterangan |
|-----|-----------|
| `APP_ENV` | `production` atau `development` |
| `PORT` | **opsional**, default `8080` |
| `HOST_URL` | Base URL publik service ini; Cloud Tasks callback ke `HOST_URL/ava/awaken` |
| `CORS_ALLOWED_ORIGIN` | SATU origin (bukan daftar) yang boleh memanggil API dari browser, dicocokkan persis; harus origin tempat SPA disajikan |
| `WEB_APP_URL` | Origin web app SPA; backend menurunkan redirect URI tiap integrasi (`WEB_APP_URL/{gworkspace,spotify}/link/callback`) darinya — tiap URL wajib terdaftar verbatim di provider-nya |
| `FIREBASE_PROJECT_ID` | Project ID Firebase untuk verifikasi ID token |
| `GOOGLE_APPLICATION_CREDENTIALS` | **opsional di GCP** — path service account credentials untuk run lokal; di Cloud Run pakai ADC |
| `GCP_PROJECT_ID` | GCP project ID (Cloud Tasks, Firestore) |
| `CLOUD_TASKS_LOCATION_ID` | Cloud Tasks location |
| `CLOUD_TASKS_QUEUE_ID` | Cloud Tasks queue ID |
| `FIRESTORE_DATABASE_ID` | Database ID Firestore — `tuya_accounts`, `gworkspace_tokens`, `spotify_tokens` |
| `ZEP_API_KEY` | API key Zep (thread + knowledge graph) |
| `GEMINI_API_KEY` | API key model Gemini — model ID di-pin di `main.go`, bukan env |
| `TUYA_ACCESS_ID` / `TUYA_ACCESS_SECRET` / `TUYA_BASE_URL` | Kredensial Tuya cloud (zee) |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | OAuth client Google Workspace (rafal + linking) |
| `SPOTIFY_CLIENT_ID` / `SPOTIFY_CLIENT_SECRET` | OAuth app Spotify (yori + linking) |
| `OAUTH_STATE_SECRET` | Secret HMAC penanda-tangan OAuth state — satu untuk semua integrasi (domain separation via nama integrasi di mac) |
| `POSTERA_DB_URL` | PostgreSQL connection string untuk postera |
| `POSTERA_API_KEY` | Shared secret postera — dipasang queue sebagai header `api-key`, diverifikasi di `POST /ava/awaken` |
| `THIRD_PARTY_API_KEY` | API key klien pihak ketiga — autentikasi `POST /ava/voice` |
| `WALLET_DB_URL` | PostgreSQL connection string untuk wallet — database KHUSUS wallet (tabel tanpa prefix), TERPISAH dari postera |
| `MIDTRANS_SERVER_KEY` / `MIDTRANS_BASE_URL` | **parked** — hanya perlu kalau top-up diaktifkan lagi |

---

# BAGIAN 6 — CHECKLIST SEBELUM MENYATAKAN SELESAI

1. `gofmt -l .` → tidak ada output.
2. `go build ./... && go vet ./...` → bersih.
3. `go test ./... -count=1` → lulus. Ingat cakupannya: `wallet` TIDAK punya test sama
   sekali (§1.5), jadi hijau di sini bukan bukti jalur SQL-nya benar.
4. Graf import masih forest. Dua aturan, dan bedanya penting:
   - **Antar package `internal/`: subpackage HANYA boleh import package induknya,
     TITIK** (`agent/*` → `agent`, `link/*` → `link`, adapter → kontrak induknya). NOL
     edge lintas fitur — `internal/agent` tidak boleh kenal `internal/session`, dst.
     Dan **memindahkan edge lintas fitur ke subpackage baru BUKAN cara memenuhi aturan
     ini**; itu cuma menyembunyikannya, sudah pernah dicoba lalu dibatalkan (lihat
     BAGIAN 3 §wallet).
   - **Import ke `wallet` (root) BUKAN edge lintas fitur** dan tidak dihitung: dia
     library platform, bukan fitur sederajat. Arahnya wajib satu arah — `internal/*`
     boleh import `wallet`, `wallet` TIDAK BOLEH import apa pun dari `internal/`. Kalau
     wallet sampai perlu tahu isi `internal/`, dia berhenti jadi library dan aturan
     ini yang dilanggar duluan.

   Cek:
   `for p in $(go list ./internal/... ./wallet/...); do go list -f '{{.ImportPath}} -> {{join .Imports " "}}' $p | tr ' ' '\n' | grep avagenc/chat; done`
   atau lebih terbaca, lihat tabel graf import di README.md dan bandingkan.
5. Route baru: gate middleware-nya sudah benar DAN **urutannya** sudah benar?
6. Kalau struktur/route/env berubah, file INI dan README.md ikut berubah di commit yang
   sama. **CLAUDE.md yang drift dari kode adalah sinyal terburuk yang bisa diberikan
   repo ini** — reviewer (manusia maupun AI) membacanya sebagai kebenaran, lalu menilai
   kode berdasarkan peta yang salah.
7. Commit message mengikuti gaya yang sudah ada: `feat(scope):`, `fix(scope):`,
   `refactor(scope):`, `chore(deps):`, `docs:`, `ci:`. Ringkas, boleh bahasa Indonesia.
