# Linking

Fitur user connect/disconnect akun eksternal. Satu subpackage per integrasi di
`internal/link/` (gworkspace, spotify), wiring di `cmd/http/main.go`
(section `2. LINKING`). Shared code yang ditemukan identik lintas integrasi —
OAuth state HMAC (`SignState`/`VerifyState`/`StateTTL`) — hidup di root
`internal/link` (`state.go`).

Terpisah dari agent: agent hanya *konsumen* token (rafal via
`*gworkspace.Client`, yori via `*spotify.Client` yang same); linking yang
mengelola grant-nya. Agent tidak tahu-menahu soal flow connect.

## Google Workspace

Implementasi di `internal/link/gworkspace/` (package `gworkspace`:
`handler.go`), di atas `go.naturallyfunny.dev/gworkspace v0.5.0`.

### Keputusan & asumsi

Pertanyaan arsitektur flow tidak terjawab, jadi diambil implementasi paling
idiomatic untuk arsitektur SPA + JSON API:

1. **Google me-redirect browser ke halaman FRONT END, bukan ke API.**
   Konsekuensinya `code` sampai ke API lewat request FE yang ber-JWT biasa,
   sehingga seluruh endpoint linking tetap di belakang auth Firebase — tidak
   ada endpoint publik baru, tidak ada session store untuk korelasi callback.
   Kalau ternyata FE tidak bisa/tidak mau punya halaman callback (mis. mobile
   app tanpa deep link), kabari — flow harus diubah ke callback backend.
2. **State stateless, HMAC-signed, diikat ke user DAN integrasi.** Format
   `integration.unixExpiry.base64url(HMAC-SHA256(integration|userID|expiry))`,
   SATU secret bersama semua integrasi dari env `OAUTH_STATE_SECRET`, TTL
   **15 menit**. Ini menutup OAuth CSRF (korban dijebak menyelesaikan flow
   dengan code milik penyerang) tanpa simpan state di server; nama integrasi
   di dalam mac men-domain-separate secret bersama itu (state gworkspace
   tidak pernah lolos verifikasi di endpoint spotify). Segmen pertama sengaja
   terbaca: halaman callback FE memakainya untuk routing (lihat poin
   berikut) — selebihnya round-trip verbatim.
3. **Satu halaman callback FE untuk semua integrasi** (env
   `LINKING_REDIRECT_URL`, mis. `/linking/callback`). Provider mana pun
   me-redirect ke halaman yang sama; halaman membaca segmen pertama `state`
   (`state.split('.')[0]` → `gworkspace` | `spotify`) lalu POST ke
   `/{integration}/connection`. URL ini wajib terdaftar verbatim di SEMUA
   provider (Google Cloud Console dan Spotify Developer Dashboard).
4. **Scope = gabungan Calendar + Gmail + Contacts** (mengikuti wiring rafal).
   Satu consent screen meminta semuanya sekaligus; kalau user meng-uncheck
   salah satu, connect ditolak (400) dan flow harus diulang. Ini disengaja:
   satu refresh token per user untuk seluruh Workspace.
5. **Reconnect diperbolehkan** — connect saat sudah connected menimpa refresh
   token lama (auth URL memakai `prompt=consent` sehingga Google selalu
   mengeluarkan refresh token baru).
6. **Path kebab-case**: `/gworkspace/auth-url`, `/gworkspace/connection`
   (connection sebagai resource: POST membuat, DELETE menghapus).

### Kontrak API (untuk front end)

Semua endpoint di belakang auth Firebase — kirim `Authorization: Bearer <ID token>`.

#### 1. Minta consent URL

```
GET /gworkspace/auth-url
→ 200 {"url": "https://accounts.google.com/o/oauth2/auth?...&state=..."}
```

Buka `url` tersebut (redirect/tab baru). URL ini hangus 15 menit setelah
di-mint (state expire) — mint tepat sebelum dipakai, jangan di-cache.

#### 2. Terima callback dari Google

Setelah user setuju, Google me-redirect browser ke halaman callback FE
bersama yang di-set di env `LINKING_REDIRECT_URL`, dengan query param:

```
https://<halaman-callback-fe>?code=4/0A...&state=gworkspace.1751...abc
```

Halaman callback FE mengambil `code` dan `state` dari query param apa
adanya, lalu routing dari segmen pertama `state`
(`state.split('.')[0]` → di sini `gworkspace`). Kalau Google mengirim
`?error=access_denied` (user menekan cancel), tampilkan pesan gagal —
tidak perlu memanggil API.

#### 3. Selesaikan connect

```
POST /gworkspace/connection
{"code": "<dari query param>", "state": "<dari query param>"}

→ 204                        sukses, akun terhubung
→ 400 invalid or expired state      state rusak/kedaluwarsa/bukan milik user ini → ulang dari step 1
→ 400 google did not grant ...      user meng-uncheck permission di consent screen → ulang, minta user centang semua
→ 400 google rejected the authorization code   code kedaluwarsa/sudah dipakai (code single-use!) → ulang dari step 1
→ 502                        error infrastruktur (Google/Firestore) → boleh retry
```

Kirim segera setelah callback — `code` dari Google single-use dan berumur
pendek (menit). Jangan retry POST dengan code yang sama setelah non-5xx.

#### 4. Cek status koneksi

```
GET /gworkspace/connection
→ 200 {"connected": true|false}
```

Kebenaran dari storage saja (ada/tidaknya refresh token tersimpan) — tidak
mem-probe grant ke Google, jadi token yang dicabut user di sisi Google tetap
terbaca `connected` sampai pemakaian pertama yang gagal.

#### 5. Disconnect

```
DELETE /gworkspace/connection
→ 204   token dihapus
→ 404   memang belum connect
```

### Catatan & issue

1. **Disconnect tidak me-revoke grant di sisi Google** — hanya menghapus
   refresh token tersimpan. Aplikasi tetap tercantum di
   myaccount.google.com/permissions sampai user mencabutnya sendiri.
   Revocation via `https://oauth2.googleapis.com/revoke` belum ada di module
   gworkspace; layak jadi follow-up, apalagi untuk kepatuhan verifikasi
   Google (lihat poin 3).
2. **Status koneksi**: `GET /{integration}/connection` →
   `{"connected": bool}` (gworkspace ≥ v0.6.0 / spotify ≥ v0.8.0 via
   `Client.Connected`). Kebenaran storage saja — tidak mem-probe provider.
3. **Gmail adalah restricted scope.** OAuth app wajib melewati verifikasi
   Google sebelum bisa dipakai user umum di production; selama development
   daftarkan penguji sebagai *test users* di OAuth consent screen.
4. **Env baru wajib di-set** (dua-duanya fatal kalau kosong; dipakai bersama
   semua integrasi linking):
   - `LINKING_REDIRECT_URL` — halaman callback FE bersama, wajib terdaftar
     **verbatim** sebagai authorized redirect URI di OAuth client Google
     Cloud Console (beda trailing slash pun ditolak Google) DAN di app
     Spotify Developer Dashboard.
   - `OAUTH_STATE_SECRET` — string random panjang (mis.
     `openssl rand -base64 32`), satu untuk semua integrasi (domain
     separation via nama integrasi di mac); mengganti secret membatalkan
     semua state yang sedang beredar (efeknya cuma user harus mengulang
     flow).
5. Kapabilitas disconnect ditambahkan ke module gworkspace sebagai
   `TokenStore.DeleteRefreshToken` + `Client.Disconnect` di **v0.5.0**
   (implementasi Firestore & Postgres) — sudah di-tag dan di-push ke main.

## Spotify

Implementasi di `internal/link/spotify/` (package `spotify`: `handler.go`),
di atas `go.naturallyfunny.dev/spotify v0.7.0`. Konsumen tokennya yori.

Flow, keputusan, dan kontrak API **sama persis** dengan Google Workspace —
ganti prefix `/gworkspace` → `/spotify`:

```
GET    /spotify/auth-url     → 200 {"url": "https://accounts.spotify.com/authorize?...&state=..."}
GET    /spotify/connection   → 200 {"connected": bool}
POST   /spotify/connection   {"code", "state"} → 204 | 400 (state/scopes/code) | 502
DELETE /spotify/connection   → 204 | 404 (belum connect)
```

Spotify me-redirect ke halaman callback FE bersama yang sama
(`LINKING_REDIRECT_URL`) dengan `?code=&state=spotify.…` (atau
`?error=access_denied` kalau user menekan cancel — tampilkan gagal, tidak
perlu memanggil API); halaman routing dari segmen pertama `state`.

Deltas dari Google Workspace:

1. **Scope** = `spotify.RequiredScopes`: `user-modify-playback-state`,
   `user-read-playback-state`, `playlist-read-private`. Semua wajib di-grant —
   kurang satu → 400, ulang flow.
2. **Env baru wajib di-set** (fatal kalau kosong):
   - `SPOTIFY_CLIENT_ID` / `SPOTIFY_CLIENT_SECRET` — OAuth app di Spotify
     Developer Dashboard. (`LINKING_REDIRECT_URL` dan `OAUTH_STATE_SECRET`
     dipakai bersama — lihat section Google Workspace; daftarkan
     `LINKING_REDIRECT_URL` verbatim juga di app dashboard Spotify.)
3. **Disconnect tidak me-revoke grant di sisi Spotify** — aplikasi tetap
   tercantum di spotify.com/account/apps sampai user mencabutnya sendiri.
4. **App Spotify default development mode** — hanya user yang di-allowlist di
   dashboard yang bisa connect, sampai app di-submit untuk extended quota.
5. Kapabilitas disconnect ditambahkan ke module spotify sebagai
   `TokenStore.DeleteRefreshToken` + `Client.Disconnect` di **v0.7.0**
   (implementasi Firestore & Postgres) — sudah di-tag dan di-push ke main.
   Playback butuh Spotify Premium (`ErrPremiumRequired` saat run yori), tapi
   itu urusan runtime agent, bukan linking.
