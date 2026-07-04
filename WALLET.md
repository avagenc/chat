# Wallet — saldo, ledger, billing token

## Konteks awal

Wallet ini lahir dari prompt berikut (Juli 2026, parafrase):

> Saya belum pernah develop software dengan transaksi/saldo. Saya mau bikin module
> yang urus saldo user, pakai Postgres. Harus:
> 1. Top-up (payment gateway belum ada, dev seeding dulu)
> 2. Potong saldo sesuai token usage tiap agent run (Ava, Zee, Rafal, maupun
>    specialist yang di-run oleh Ava)
> 3. Tidak bisa refund
> 4. Entry = log pemakaian, bukan tabel terpisah
> 5. Harga per token di-inject (bukan di DB)
> 6. Pakai rupiah, bukan token (biar gampang jadi e-wallet nanti)
> 7. Decoupled ready — avagenc-level, bukan avagenc-chat-specific
> 8. Kind ready — support transaksi non-token di masa depan
> 9. Saya abu-abu soal akuntansi, tanyakan saya kalau perlu
>
> Asumsi jangka panjang: Avagenc bisa jadi e-wallet beneran — bayar sesuatu,
> transfer antar pengguna — bukan cuma top-up & potong saldo token.

Versi awal (single-entry) diimplementasikan tanpa menanyakan poin 9 — sebuah
keputusan yang perlu dikoreksi. Dokumen ini mencatat hasil diskusi lanjutan,
alasan di balik setiap keputusan, dan **sudah diimplementasikan** (Juli 2026):
karena wallet belum rilis, skema double-entry menggantikan skema single-entry
lewat drop + recreate (`0002_double_entry.sql`), tanpa jalur migrasi data.

## Ringkasan arsitektur

Satu vertical slice di `internal/wallet/`:
- **`ledger.go`** — port `Ledger` (kontrak double-entry: `Transact`), tipe domain
  (`Posting`, `Spec`, `Transaction`, `Entry`, `Kind`), helper `UserAccountID`
- **`biller.go`** — `Biller` yang mengubah token usage jadi satu transaksi
  balanced (debit user, credit revenue)
- **`guard.go`** — `RequireBalance` middleware (402)
- **`handler.go`** — endpoint baca saldo & usage
- **`postgres/`** — adapter pgx, implementasi konkret `Ledger`

Wiring di `cmd/http/server.go` (section `1. 3. Billing` dan `4. WALLET`).

Wallet adalah **fitur first-party Avagenc**, bukan library — karena itu ia
tinggal di `internal/`, bukan package public (kontras dengan keluarga `memory/`
yang memang library portable). Kontrak `Ledger`-nya tetap tidak menafsirkan isi
transaksi — `Kind` dan `Metadata` milik konsumen (kind baru: `api_call`,
`payment`, `transfer`, dst) — jadi kalau kelak dibutuhkan produk Avagenc lain,
ekstraksinya mekanis: angkat `ledger.go` + `postgres/` ke module sendiri
(`go.avagenc.com/wallet`) atau jadikan microservice. Untuk saat ini (belum ada
produk Avagenc kedua yang butuh wallet), `internal/` sudah benar — menghindari
komitmen API publik yang belum teruji.

### Kenapa double-entry, bukan single-entry?

**Versi awal single-entry** — setiap transaksi hanya mencatat perubahan saldo
satu akun (user). Contoh: `Debit(user, 1000)` menulis entry `-1000` untuk user
dan tidak mencatat apa pun di sisi lain. Cukup untuk billing sederhana, tapi
**tidak bisa direkonsiliasi**: kalau ada bug (misal debit Rp1.000.000 padahal
cuma Rp10.000), tidak ada cross-check otomatis — uang "hilang" tanpa jejak di
sisi revenue.

**Double-entry** — setiap transaksi mencatat minimal dua entry yang jumlahnya
nol. Debit user `-1000` harus dibarengi credit revenue `+1000`. Total semua
akun selalu nol. Kalau ada selisih, langsung ketahuan. Ini standard industri
untuk Stripe, Xendit, ledger perbankan, dan semua sistem yang menangani uang
sungguhan.

| Aspek | Single-entry | Double-entry |
|-------|-------------|--------------|
| Rekonsiliasi otomatis | Tidak | Ya (SUM semua akun = 0) |
| Deteksi bug | Manual | Otomatis (invarian) |
| Kompleksitas | Rendah | Sedang |
| Audit trail | Satu sisi | Dua sisi (lengkap) |
| Ekstraksi ke e-wallet | Refactor besar | Tinggal tambah akun bank/pending |
| Transfer antar user | Tidak terekam utuh | Satu transaksi `transfer` dua postings |

Karena wallet ini direncanakan untuk berevolusi jadi e-wallet Avagenc (bukan
cuma token billing), double-entry adalah pilihan yang tepat sejak awal.
Migrasi sekarang lebih murah daripada nanti.

## Keputusan desain

### 1. Double-entry: `Transact`, bukan `Debit`/`Credit`

**Keputusan:** Port `Ledger` menyediakan satu method
`Transact(ctx, Spec) (*Transaction, error)`. `Spec` = kind + ref + metadata +
≥ 2 `Posting` yang jumlah `Amount`-nya nol — dilanggar → error. Semua postings
ditulis dalam satu transaksi SQL atomik.

**Alasan:** Method `Debit`/`Credit` tidak masuk akal untuk double-entry karena
setiap transaksi selalu melibatkan ≥ 2 akun. Method `Transact` eksplisit
menggambarkan kenyataan akuntansi: "satu transaksi = satu set postings yang
balanced". Konsumen (biller, webhook top-up, transfer antar user) bebas
menentukan pasangan akunnya.

**Contoh:**

```go
// Biaya agent run Rp1.250 (biller.go melakukan persis ini)
ledger.Transact(ctx, wallet.Spec{
    Kind:     "agent_run",
    Metadata: receiptJSON,
    Postings: []wallet.Posting{
        {AccountID: wallet.UserAccountID("abc123"), Amount: -1_250_000},
        {AccountID: wallet.AccountRevenue, Amount: +1_250_000},
    },
})

// Top-up Rp100.000 (nanti oleh payment gateway webhook)
ledger.Transact(ctx, wallet.Spec{
    Kind: "topup",
    Ref:  "order-xxx",
    Postings: []wallet.Posting{
        {AccountID: wallet.UserAccountID("abc123"), Amount: +100_000_000_000},
        {AccountID: "pending", Amount: -100_000_000_000},
    },
})

// Transfer antar user (nanti): tidak butuh perubahan kontrak
ledger.Transact(ctx, wallet.Spec{
    Kind: "transfer",
    Ref:  "transfer-zzz",
    Postings: []wallet.Posting{
        {AccountID: wallet.UserAccountID("pengirim"), Amount: -50_000_000_000},
        {AccountID: wallet.UserAccountID("penerima"), Amount: +50_000_000_000},
    },
})
```

### 2. Konvensi tanda: credit-positive

**Keputusan:** Satu kolom `amount` bertanda (bukan pasangan kolom
debit/credit). Positif = uang masuk ke akun, negatif = keluar. Konsekuensinya:
saldo user dan revenue terbaca positif (intuitif), sementara akun bertipe aset
(`pending`, nanti `bank`) menumpuk saldo **negatif** — itu cermin double-entry
(convention "credits positive"), bukan bug. Total seluruh akun selalu nol.

**Alasan:** Kolom tunggal bertanda + invarian SUM = 0 adalah simplifikasi yang
lazim di ledger fintech dan cukup untuk e-wallet; pasangan kolom debit/credit
dengan normal balance per tipe akun (idiom akuntansi formal) menambah
kompleksitas tanpa manfaat di skala ini. Yang penting konvensinya DITULIS —
tanpa ini, saldo `pending` yang negatif akan terlihat seperti bug.

### 3. Journal header: tabel `wallet_transactions`

**Keputusan:** Transaksi = satu baris header (`wallet_transactions`: `id`,
`kind`, `ref`, `metadata`, `created_at`) + ≥ 2 baris lines (`wallet_entries`:
`txn_id`, `account_id`, `amount`, `balance_after`). `kind`, `ref`, dan
`metadata` hidup di header, BUKAN di entry.

**Alasan (revisi dari desain sebelumnya):** Draft awal menaruh `txn_id`
sebagai kolom biasa di entries dan `ref`/`kind`/`metadata` per-entry. Itu
ambigu: posting mana yang membawa `ref` idempotensi? kenapa `kind` terduplikasi
di tiap posting? metadata milik siapa? Idempotensi webhook adalah properti
*transaksi* (retry order yang sama = transaksi yang sama), begitu pula kind dan
receipt. Header + lines adalah idiom jurnal akuntansi (dan skema ledger
komersial: Modern Treasury, Stripe Ledger). Invarian per transaksi: untuk
setiap `txn_id`, `SUM(amount) = 0`. Sisi baca tetap ergonomis: `Entries`
me-join header sehingga `wallet.Entry` tetap membawa `Kind`/`Ref`/`Metadata`.

### 4. Chart of accounts

**Keputusan:** Tiga akun sistem — semuanya di-seed migration.

| ID | Type | Fungsi |
|----|------|--------|
| `user:{uid}` | `user` | Saldo user. Jumlah semua akun user = total dana yang dipegang untuk user (liability) |
| `revenue` | `system` | Revenue company dari pemakaian (agent run, nanti API call, dll) |
| `pending` | `system` | Settlement payment gateway — dana yang belum settle. Sekarang juga pasangan dev seeding |

**Alasan:** Minimal namun cukup. Revenue menjadi pasangan setiap debit
pemakaian. Pending menjadi pasangan setiap top-up dari gateway — settlement
berarti memindahkan dari pending ke akun bank (nanti). Chart of accounts
adalah open set — akun baru (`bank`, `expense`, escrow) tinggal INSERT di
migration.

**Mengapa tidak ada akun bank sekarang:** Belum ada payment gateway. Akun bank
(balance perusahaan beneran) akan ditambah ketika gateway online.

### 5. Account ID: `type` + `user_id` terpisah

**Keputusan:**
- `id TEXT PK` — untuk user akun: `user:{firebaseUID}`; untuk system: `revenue`, `pending`
- `type TEXT` — `'user'` atau `'system'`, CHECK constraint
- `user_id TEXT nullable` — wajib diisi untuk type `'user'`, wajib NULL untuk
  system (CHECK `(type = 'user') = (user_id IS NOT NULL)`)

**Alasan:** Kolom `user_id` terpisah memungkinkan query SQL seperti
`WHERE type = 'user'` atau `WHERE user_id = 'abc'` tanpa parsing string. ID
tetap deterministik (`user:{uid}`) sehingga lookup user balance tidak perlu
query — langsung konstruksi ID via `wallet.UserAccountID`.
`UNIQUE (user_id) WHERE user_id IS NOT NULL` mencegah duplikat akun user.

### 6. Refund

**Keputusan:** Kind `refund` disediakan dari awal. Refund = transaksi
double-entry yang membalik arah aliran uang (credit user, debit revenue).

**Alasan:** Walaupun prompt awal berkata "tidak bisa refund", dengan kehadiran
payment gateway di masa depan, dispute dan refund adalah realitas bisnis.
Menyediakan struktur dari awal mencegah skema darurat (INSERT manual di DB)
yang rawan error. Refund policy tetap bisa "no refund" di level aplikasi —
struktur ledger tetap siap kalau policy berubah.

### 7. Satuan micro-rupiah, BIGINT

1 IDR = 1.000.000 micros. Aritmetika integer eksak: dengan tarif dalam *rupiah
per juta token*, `micros = tarif × token` — tanpa pembagian, tanpa float, tanpa
rounding di jalur tulis. API menampilkan rupiah bulat (truncate ke bawah —
user tidak pernah melihat lebih dari yang dia punya); field `*_micros`
otoritatif. Single-currency IDR by design; kalau kelak multi-currency, tambah
kolom `currency` + akun per currency — jangan spekulasi sekarang.

### 8. Akun user implisit, akun system di-seed

Tidak ada `ErrAccountNotFound` dan tidak ada endpoint buat akun: user tanpa
baris account = saldo 0. Baris dibuat oleh posting pertama (upsert di dalam
`Transact`, dikenali dari prefix `user:`). Akun system TIDAK auto-create —
di-seed di migration; posting ke akun system yang tidak ada = bug wiring,
bukan kondisi yang di-heal diam-diam. Tidak ada pencegahan debit melebihi
saldo — billing write tidak boleh hilang hanya karena dana habis.

### 9. Post-paid

Biaya run baru diketahui setelah run selesai, jadi debit selalu setelah run.
Gate hanya mensyaratkan saldo > 0 untuk *memulai* run; run terakhir boleh
membuat saldo sedikit minus. Tidak ada refund di level kebijakan (struktur
tetap siap).

### 10. Gate 402 di semua pintu agent

`/ava`, `/zee`, `/rafal`, dan `/ava/awaken`. Untuk awaken (callback Cloud
Tasks), 402 berarti Cloud Tasks me-retry task sampai retry policy queue habis
— diterima dulu; alternatifnya (balas 200 dan drop) menukar noise retry dengan
wake-up yang hilang diam-diam.

### 11. Transaksi = usage log

Satu transaksi (`agent_run`) per `runner.Run`. Metadata transaksi berisi
`wallet.Receipt`: agent, session, trigger, model, breakdown token, snapshot
tarif. Tidak ada tabel log terpisah — endpoint usage membaca entries akun user
(join ke header untuk receipt), cost dihitung dari `amount` entry sehingga
kebal drift bentuk receipt. Ava yang mendelegasikan ke specialist menghasilkan
**dua transaksi** (dua `runner.Run` berbeda) — memang benar, bukan
double-charge.

### 12. Tarif di-inject, hardcoded

`wallet.Price` (rupiah per juta token) di-inject eksplisit di `server.go` ke
`wallet.NewBiller`. Hardcoded, bukan env: snapshot-nya tercatat di tiap
transaksi, dan ubah tarif = keputusan deploy. `PromptTokenCount` Gemini SUDAH
termasuk cached, jadi billed input = `Prompt + ToolUsePrompt − Cached`
(clamp ≥ 0).

### 13. Idempotency via `Ref` di level transaksi

Unique index partial di `wallet_transactions.ref` (`WHERE ref IS NOT NULL`);
tabrakan → sentinel `wallet.ErrDuplicateRef`. Ini hook untuk webhook payment
gateway nanti: retry transaksi (top-up, refund, transfer) dengan order ID yang
sama terdeteksi dengan `errors.Is` — dan karena ref milik header, retry
menolak SELURUH transaksi, bukan sebagian postings.

### 14. Charge di semua exit path

`Charge` dipanggil via `defer` di tiap drain loop: token yang terpakai sebelum
error iterator/502 tetap dibayar. Charge gagal = log, bukan 5xx — response ke
user tidak dikorbankan demi billing. `Charge` memakai `context.WithoutCancel`
supaya debit tetap tertulis walau client disconnect setelah token terlanjur
terpakai.

### 15. Concurrency: lock order deterministik, hot account sebagai lever skala

`Transact` mengunci akun dalam **urutan account ID** (postings di-sort sebelum
di-apply). Dua transaksi konkuren yang menyentuh akun yang sama selalu
mengambil row lock dalam urutan yang sama → antre, bukan deadlock.

Konsekuensi yang disadari: setiap `agent_run` mengunci baris `revenue`,
jadi semua charge konkuren terserialisasi sebentar di baris itu. Di Postgres,
lock yang dipegang mikro-detik dalam transaksi pendek sanggup ribuan
transaksi/detik — jauh di atas kebutuhan. Kalau suatu hari benar-benar jadi
bottleneck, lever standarnya sudah dikenal (sub-akun revenue yang di-shard
lalu di-roll-up, atau saldo system account dihitung dari SUM entries tanpa
baris termaterialisasi) — keputusan itu ditunda dengan sengaja, bukan
dilupakan.

## Skema DB (`internal/wallet/postgres/migrations/`)

Wallet punya **database sendiri** (bukan share dengan postera), jadi tabel
TANPA prefix. Karena belum pernah rilis, satu migrasi bersih:

- `0001_initial.sql` — drop sisa tabel `wallet_*` dari iterasi terdahulu
  (kalau ada), lalu buat skema double-entry: `accounts` (id/type/user_id/
  balance + CHECKs), `transactions` (header: kind/ref/metadata), `entries`
  (lines: txn_id/account_id/amount ≠ 0/balance_after), seed akun `revenue` +
  `pending`.

**Invarian transaksi:** `SELECT SUM(amount) FROM entries WHERE txn_id = $1` → 0
**Invarian account:** `accounts.balance = SUM(entries.amount)` per account
**Invarian global:** `SELECT SUM(balance) FROM accounts` → 0

Setiap `Transact` di Postgres: 1 transaksi SQL → INSERT header (ref unik →
`ErrDuplicateRef`) → per posting terurut: upsert/update account (row lock) →
INSERT entry dengan `balance_after` hasil lock itu → commit.

**Migrasi milik pipeline deploy, bukan boot path** — runtime cukup privilege
DML. Step `Migrate Wallet Database` di `.github/workflows/deploy.yaml`
menjalankan goose (version ledger di tabel default `goose_db_version` — aman
karena database khusus wallet) sebelum Deploy to Cloud Run. Saat boot,
`NewLedger` hanya memvalidasi tabel ada.

Menjalankan manual:
```sh
go run github.com/pressly/goose/v3/cmd/goose@v3.27.2 \
  -dir internal/wallet/postgres/migrations \
  postgres "$WALLET_DB_URL" up
```

Env: `WALLET_DB_URL` — database khusus wallet dengan pool pgx sendiri,
terpisah dari postera.

## Kontrak API (untuk front end)

Semua di belakang auth Firebase, TANPA gate saldo (wallet kosong harus tetap
bisa melihat saldonya).

- `GET /wallet` →
  `200 {"balance": 100000, "balance_micros": 100000000000}`
  `balance` = rupiah bulat (truncate); user tanpa akun dapat 0, bukan 404.
- `GET /wallet/usage/today` — **wajib header `time-zone`** (mis.
  `Asia/Jakarta`; "hari ini" dihitung dari midnight lokal) →
  `200 {"tokens": 52480, "cost": 4120, "cost_micros": 4120000000}`
- Semua route agent (`POST /ava`, `/zee`, `/rafal`) bisa membalas
  `402 {"detail": "insufficient balance"}` — UI harus menanganinya
  (arahkan ke top-up).

## Dev seeding (belum ada top-up)

Seeding = transaksi `topup` balanced: credit user, debit `pending` (persis
seperti yang nanti dilakukan webhook gateway). Ganti `<FIREBASE_UID>` dan
`ref` tiap seeding (unique).

```sql
BEGIN;
WITH txn AS (
    INSERT INTO transactions (kind, ref, metadata)
    VALUES ('topup', 'dev-seed-0001', '{"source":"dev-seed"}')
    RETURNING id
), user_acct AS (
    INSERT INTO accounts (id, type, user_id, balance)
    VALUES ('user:<FIREBASE_UID>', 'user', '<FIREBASE_UID>', 100000::bigint * 1000000)
    ON CONFLICT (id) DO UPDATE
        SET balance = accounts.balance + EXCLUDED.balance, updated_at = now()
    RETURNING id, balance
), pending_acct AS (
    UPDATE accounts
    SET balance = balance - 100000::bigint * 1000000, updated_at = now()
    WHERE id = 'pending'
    RETURNING id, balance
)
INSERT INTO entries (txn_id, account_id, amount, balance_after)
SELECT txn.id, u.id,  100000::bigint * 1000000, u.balance FROM txn, user_acct u
UNION ALL
SELECT txn.id, p.id, -100000::bigint * 1000000, p.balance FROM txn, pending_acct p;
COMMIT;
```

Cek invarian kapan pun:
```sql
-- Invarian transaksi: jumlah tiap txn_id harus nol
SELECT txn_id, SUM(amount) FROM entries
GROUP BY txn_id HAVING SUM(amount) <> 0;

-- Invarian account: saldo = sum entries (akun tanpa entry harus 0)
SELECT a.id FROM accounts a
LEFT JOIN (SELECT account_id, SUM(amount) s FROM entries GROUP BY account_id) e
    ON a.id = e.account_id
WHERE a.balance <> COALESCE(e.s, 0);

-- Invarian global: total seluruh saldo nol
SELECT SUM(balance) FROM accounts;
```

## Rencana

1. **Top-up Midtrans (nanti):** endpoint create transaction (balas link/snap
   token) + webhook notification → `Transact` `topup` (credit user, debit
   pending). `Ref` = order ID Midtrans; `ErrDuplicateRef` → balas 200.
2. **Settlement:** webhook settlement dari gateway → `Transact` pindahkan dari
   pending ke akun bank (akun baru `bank` atau `cash`, seed via migration).
3. **Transfer antar user:** kind `transfer`, dua postings antar akun user —
   kontrak sudah mendukung, tinggal endpoint + policy (limit, PIN, dsb).
4. **Ekstraksi microservice:** kontrak `Ledger` tidak berubah, hanya adapter
   postgres diganti REST/gRPC client.
5. **`wallet.Receipt`** adalah kontrak tulis/baca antara biller dan endpoint
   usage — sejak keduanya sepackage, drift field terlihat di satu tempat; cost
   tetap kebal karena dihitung dari `amount` entry, bukan metadata.
