# Wallet — PRD

Saldo user, ledger double-entry, dan billing pemakaian token untuk platform
Avagenc. Fitur first-party level Avagenc (bukan spesifik avagenc-chat), saat
ini hidup sebagai vertical slice di `internal/wallet/` pada service chat.

## Tujuan

1. Menyimpan saldo user dalam rupiah dan memotongnya sesuai token usage tiap
   agent run (Ava, Zee, Rafal, Yori, maupun specialist yang di-run Ava).
2. Siap berevolusi menjadi e-wallet Avagenc sungguhan — seperti gopay, shopeepay, dana, ovo.
3. Setiap rupiah bisa direkonsiliasi: tidak ada uang "hilang" tanpa jejak.
4. Siap menjadi multi-currency, tidak boleh terikat ke rupiah, tidak perlu ada migrasi besar hanya untuk support multi-currency.

Goal saat ini: avagenc user bisa top up web avagenc chat (dev/avagenc/chat-web), semua tercatat dengan praktik wallet sempurna sesua tujuan jangka panjang di atas, dan tentu dengan akuntansi yang purist idiomatic tidak pragmatic dan workaround. Goal saat ini tidak berarti mengabaikan goal jangka panjang. System aharus dibangun sesuai goal jangka panjang, bukan goal saat ini.

## Arsitektur

Satu vertical slice di `internal/wallet/`:

| File | Isi |
|------|-----|
| `ledger.go` | Port `Ledger` (kontrak double-entry: `Transact`), tipe domain (`Posting`, `Spec`, `Transaction`, `Entry`, `Kind`), helper `UserAccountID` |
| `biller.go` | `Biller` — token usage → satu transaksi balanced (debit user, credit revenue) |
| `guard.go` | `RequireBalance` middleware (402) |
| `handler.go` | Endpoint baca saldo & usage |
| `midtrans/` | Top-up via Midtrans Snap: create transaction + webhook notification (dikonfirmasi ke status API sebelum membukukan) → transaksi `topup` |
| `postgres/` | Adapter pgx, implementasi konkret `Ledger`, di database khusus wallet |

Kontrak `Ledger` tidak menafsirkan isi transaksi — `Kind` dan `Metadata`
milik konsumen — sehingga ekstraksi ke module/microservice terpisah nanti
bersifat mekanis (angkat `ledger.go` + `postgres/`, atau ganti adapter dengan
client REST/gRPC).

## Model ledger

### Double-entry via `Transact`

Satu-satunya cara menulis: `Transact(ctx, Spec)` — kind + ref + metadata +
≥ 2 `Posting` yang jumlah `Amount`-nya **nol** (dilanggar → error). Semua
postings ditulis dalam satu transaksi SQL atomik. Tidak ada method
`Debit`/`Credit` — setiap transaksi selalu melibatkan ≥ 2 akun, dan konsumen
(biller, webhook top-up, transfer) bebas menentukan pasangan akunnya.

```go
// Biaya agent run Rp1.250
ledger.Transact(ctx, wallet.Spec{
    Kind:     "agent_run",
    Metadata: receiptJSON,
    Postings: []wallet.Posting{
        {AccountID: wallet.UserAccountID("abc123"), Amount: -1_250_000},
        {AccountID: wallet.AccountRevenue, Amount: +1_250_000},
    },
})
```

Alasan double-entry (vs single-entry versi awal): rekonsiliasi otomatis
(SUM semua akun = 0), bug billing langsung ketahuan sebagai selisih, audit
trail dua sisi, dan transfer antar user terekam utuh sebagai satu transaksi.
Standard industri untuk sistem yang menangani uang sungguhan.

### Konvensi tanda: credit-positive

Satu kolom `amount` bertanda. Positif = uang masuk ke akun, negatif = keluar.
Saldo user dan `revenue` terbaca positif; akun bertipe aset (`pending`, nanti
`bank`) menumpuk saldo **negatif** — itu cermin double-entry, bukan bug.
Total seluruh akun selalu nol.

### Satuan: micros, BIGINT

1 unit mata uang = 1.000.000 micros (operasional sekarang: 1 IDR = 1.000.000
micros). Aritmetika integer eksak: tarif dalam *rupiah per juta token* →
`micros = tarif × token`, tanpa pembagian/float/rounding di jalur tulis. API
menampilkan rupiah bulat (truncate ke bawah); field `*_micros` otoritatif.

Satuan micros currency-agnostic — desain tidak terikat rupiah (tujuan #4).
Jalur ke multi-currency aditif, bukan migrasi besar: tambah kolom `currency`
di accounts (baris existing = `IDR`) + akun per currency, dengan aturan semua
postings satu transaksi ber-currency sama — invarian SUM = 0 hanya bermakna
dalam satu currency. Konversi antar currency = transaksi via akun FX, bukan
posting lintas currency.

### Journal: header + lines

Transaksi = satu baris header (`transactions`: `kind`, `ref`, `metadata`) +
≥ 2 baris lines (`entries`: `txn_id`, `account_id`, `amount`,
`balance_after`). `kind`/`ref`/`metadata` hidup di header, bukan per-entry —
idempotensi, kind, dan receipt adalah properti transaksi. Idiom jurnal
akuntansi (Modern Treasury, Stripe Ledger). Sisi baca me-join header sehingga
`wallet.Entry` tetap membawa `Kind`/`Ref`/`Metadata`.

### Chart of accounts

| ID | Type | Fungsi |
|----|------|--------|
| `user:{uid}` | `user` | Saldo user (liability terhadap user) |
| `revenue` | `system` | Revenue company dari pemakaian |
| `pending` | `system` | Settlement payment gateway; sekarang juga pasangan dev seeding |

Open set — akun baru (`bank`, escrow, dsb) tinggal INSERT di migration.
Kolom `type` + `user_id` terpisah dari `id` supaya query tanpa parsing
string; ID tetap deterministik (`user:{uid}`) sehingga lookup saldo user
tidak perlu query pencarian.

**Akun user implisit, akun system di-seed.** User tanpa baris account =
saldo 0; barisnya lahir dari posting pertama (upsert di `Transact`). Akun
system TIDAK auto-create — posting ke system account yang tidak ada adalah
bug wiring, bukan kondisi yang di-heal diam-diam.

### Idempotency

Unique index partial di `transactions.ref` (`WHERE ref IS NOT NULL`);
tabrakan → sentinel `wallet.ErrDuplicateRef` (dicocokkan via `errors.Is`).
Hook untuk webhook payment gateway: retry dengan order ID sama menolak
SELURUH transaksi, bukan sebagian postings.

### Concurrency

`Transact` mengunci akun dalam urutan account ID (postings di-sort dulu) —
transaksi konkuren antre, bukan deadlock. Konsekuensi: semua charge
terserialisasi sebentar di baris `revenue`; row lock mikro-detik di Postgres
sanggup ribuan txn/detik, jauh di atas kebutuhan. Lever skala kalau kelak
perlu (shard sub-akun revenue, atau saldo system dihitung dari SUM entries)
ditunda dengan sengaja.

## Billing agent run

- **Post-paid.** Biaya baru diketahui setelah run selesai, jadi debit selalu
  setelah run. Gate `RequireBalance` (saldo > 0, habis → 402) hanya untuk
  *memulai* run di `/ava`, `/zee`, `/rafal`, `/yori`, `/ava/awaken`; run
  terakhir boleh membuat saldo sedikit minus. Tidak ada pencegahan overdraft —
  billing write tidak boleh hilang karena dana habis. Untuk `/ava/awaken`
  (callback Cloud Tasks), 402 berarti Cloud Tasks retry sampai policy queue
  habis — diterima; alternatifnya wake-up hilang diam-diam.
- **Transaksi = usage log.** Satu transaksi `agent_run` per `runner.Run`,
  metadata = `wallet.Receipt` (agent, session, trigger, model, breakdown
  token, snapshot tarif). Tidak ada tabel log terpisah — endpoint usage
  membaca entries akun user; cost dihitung dari `amount` entry sehingga kebal
  drift bentuk receipt. Ava yang delegasi ke specialist = dua transaksi (dua
  `runner.Run`), bukan double-charge.
- **Charge di semua exit path.** `Charge` via `defer` di tiap drain loop:
  token yang terpakai sebelum error tetap dibayar. Charge gagal = log, bukan
  5xx. Pakai `context.WithoutCancel` supaya debit tertulis walau client
  disconnect.
- **Tarif di-inject, hardcoded.** `wallet.Price` (rupiah per juta token)
  di-inject di main, bukan env: snapshot tercatat di tiap transaksi, ubah
  tarif = keputusan deploy. Billed input Gemini = `Prompt + ToolUsePrompt −
  Cached` (clamp ≥ 0), karena `PromptTokenCount` sudah termasuk cached.
- **Refund.** Kind `refund` tersedia dari awal (credit user, debit revenue)
  — struktur siap untuk dispute payment gateway; policy aplikasi tetap
  "no refund".

## Top-up Midtrans

Slice `internal/wallet/midtrans/` — pola linking: satu subpackage per
integrasi; kind `topup` dan skema order ID milik slice ini karena dialah
satu-satunya penulisnya (gateway kedua kelak mengangkat yang TERBUKTI
identik, bukan diramal).

- **Create.** `POST /wallet/topup` (auth Firebase, TANPA gate saldo) membuat
  transaksi Snap di Midtrans dan membalas `token` (Snap.js) + `redirect_url`
  (redirect flow). TIDAK ada tulisan ledger di sini — uang baru bergerak
  saat notifikasi pembayaran datang.
- **Order ID = binding user, stateless.** `topup~{uid}~{nonce}` — order ID
  satu-satunya field yang pasti di-echo notifikasi, jadi dia sendiri yang
  membawa identitas user (ide yang sama dengan state parameter linking).
  Budget 50 char Midtrans: `topup~` (6) + UID Firebase 28 + `~` + 12 hex
  = 47; `~` legal di Midtrans dan tidak pernah muncul di UID.
- **Webhook.** `POST /wallet/topup/notification` (URL di-set di dashboard
  Midtrans) di LUAR auth Firebase — server Midtrans yang memanggil;
  diautentikasi signature SHA-512 atas
  `order_id + status_code + gross_amount + server key`.
- **Konfirmasi → ledger (jangan pernah membukukan dari body).** Signature
  hanya membuktikan keaslian SELAMA server key rahasia; kalau bocor, notifikasi
  "paid" palsu bisa lolos. Karena itu setiap body yang mengaku selesai
  (`status_code` `200` DAN `settlement`, atau `capture`+`fraud_status` `accept`
  — gate `status_code` menolak body non-sukses yang field statusnya di-edit,
  karena `status_code` DI-COVER signature tapi `transaction_status`/`fraud_status`
  tidak) dikonfirmasi dulu ke **status API Core Midtrans**
  (`GET api.../v2/{order_id}/status`, auth server key). Uang dibukukan dari
  status, amount, dan currency yang DILAPORKAN API itu — bukan dari body — jadi
  key yang bocor tidak bisa memalsukan pembukuan, dan amount yang dibukukan
  selalu yang tercatat di Midtrans. Host API diturunkan dari `MIDTRANS_BASE_URL`
  (`app.` → `api.`), bukan env terpisah, supaya tidak bisa drift antar
  environment. Hasil konfirmasi:
  - selesai → `Transact` `topup` (credit user, debit `pending`), `Ref` = order
    ID, metadata `{payment_type, transaction_id, transaction_time}` dari status.
  - body mengaku selesai tapi status API bilang belum (pending / tak ada) →
    200 tanpa tulisan (stale/forged).
  - `pending`/`deny`/`cancel`/`expire` di body → 200 tanpa tulisan, TANPA
    memanggil status API.
  - retry Midtrans (termasuk `settlement` menyusul `capture` yang sudah
    dibukukan) → `ErrDuplicateRef` → 200, seluruh transaksi ditolak utuh.
  - jawaban konfirmasi TAK KONKLUSIF (network, key salah, gateway 5xx) atau
    pembayaran sah yang tetap tak bisa dibukukan (currency non-IDR, amount tak
    terparse) → 500 supaya retry Midtrans menjaganya tetap kelihatan, bukan
    hilang diam-diam.
- **Amount eksak.** `gross_amount` desimal string (`"50000.00"`) → micros
  via aritmetika integer murni, tanpa float; create mengirim rupiah bulat
  (`amount` integer > 0 — syarat Midtrans untuk IDR).
- **Env.** `MIDTRANS_SERVER_KEY` + `MIDTRANS_BASE_URL`
  (`https://app.sandbox.midtrans.com` / `https://app.midtrans.com`).

## Skema DB & operasional

Database **khusus wallet** (`WALLET_DB_URL`, pool pgx sendiri, terpisah dari
postera), tabel tanpa prefix. Migrasi goose di
`internal/wallet/postgres/migrations/` — dijalankan **pipeline deploy**
(step `Migrate Wallet Database` di deploy.yaml) sebelum deploy; runtime
hanya memvalidasi tabel ada, tidak pernah DDL.

Setiap `Transact`: 1 transaksi SQL → INSERT header (ref unik →
`ErrDuplicateRef`) → per posting terurut: upsert/update account (row lock) →
INSERT entry dengan `balance_after` → commit.

Invarian (bisa dicek kapan pun via SQL):
- **Transaksi:** `SUM(amount)` per `txn_id` = 0
- **Account:** `accounts.balance` = `SUM(entries.amount)` per account
- **Global:** `SUM(balance)` seluruh accounts = 0

Top-up dev lewat jalur normal via Midtrans sandbox (`MIDTRANS_BASE_URL`
sandbox). Seeding SQL manual masih mungkin — transaksi `topup` balanced
(credit user, debit `pending`), `ref` unik, jaga ketiga invarian di atas —
tapi bukan lagi jalur utama.

## Kontrak API (front end)

Semua di belakang auth Firebase, TANPA gate saldo (wallet kosong tetap bisa
lihat saldonya).

- `GET /wallet` → `200 {"balance": 100000, "balance_micros": 100000000000}`
  — `balance` rupiah bulat (truncate); user tanpa akun dapat 0, bukan 404.
- `GET /wallet/usage/today` — **wajib header `time-zone`** (mis.
  `Asia/Jakarta`; "hari ini" dari midnight lokal) →
  `200 {"tokens": 52480, "cost": 4120, "cost_micros": 4120000000}`
- `POST /wallet/topup` body `{"amount": 50000}` (rupiah bulat, > 0) →
  `200 {"token": "...", "redirect_url": "...", "order_id": "topup~{uid}~{nonce}"}`
  — `token` untuk Snap.js, `redirect_url` untuk redirect flow.
- `POST /wallet/topup/notification` — BUKAN untuk front end: webhook
  server-to-server Midtrans (lihat "Top-up Midtrans").
- Semua route agent (`POST /ava`, `/zee`, `/rafal`, `/yori`) bisa membalas
  `402 {"detail": "insufficient balance"}` — UI wajib menanganinya
  (arahkan ke top-up).

## Roadmap

1. **Top-up Midtrans — ✅ selesai** (lihat "Top-up Midtrans" di atas):
   `POST /wallet/topup` (balas snap token + redirect URL) + webhook
   notification → `Transact` `topup` (credit user, debit pending), `Ref` =
   order ID Midtrans, `ErrDuplicateRef` → 200. Tinggal dikonsumsi web
   avagenc chat (`dev/avagenc/chat-web`).
2. **Settlement:** webhook settlement → `Transact` pindahkan dari `pending`
   ke akun `bank` baru (seed via migration).
3. **Transfer antar user:** kind `transfer`, dua postings antar akun user —
   kontrak sudah mendukung, tinggal endpoint + policy (limit, PIN, dsb).
4. **Multi-currency:** kolom `currency` + akun per currency (lihat Satuan) —
   aditif, tanpa migrasi besar.
5. **Ekstraksi microservice/module:** kontrak `Ledger` tetap, hanya adapter
   postgres diganti client.
