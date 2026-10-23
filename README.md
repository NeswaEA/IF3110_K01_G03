# IF3110_K01_G03

Aplikasi web untuk **mengelola akses database, menelusuri schema dan data, serta membuat dan menjalankan migrasi SQL secara terkontrol dan memiliki riwayat**.

Admin mendaftarkan database target (PostgreSQL atau SQLite) dan memberi hak akses per database melalui group. Pengguna hanya melihat database yang boleh mereka akses:

- dengan akses **lihat**, pengguna dapat membaca schema, data, dan riwayat migrasi;
- dengan akses **edit**, pengguna juga dapat menulis migrasi SQL, menganalisis rencana eksekusinya, dan menjalankannya.

Setiap migrasi berjalan dalam satu transaksi: seluruhnya di-commit bila berhasil, seluruhnya di-rollback bila gagal. Hasilnya tercatat dalam riwayat linear per database.

## Fitur

| Fitur | Keterangan |
|---|---|
| Autentikasi | Login username/password (hash Argon2id), session opaque di backend lewat cookie HttpOnly, rotasi, masa berlaku, logout/revoke, proteksi CSRF |
| Otorisasi | Hak `lihat`/`edit` per database melalui group; hak efektif adalah yang tertinggi dari semua group pengguna. Admin platform terpisah dari akses ke target |
| Registry koneksi | Admin mendaftarkan, mengubah, menguji, dan menonaktifkan koneksi. Kredensial disimpan terenkripsi (AES-GCM) dan tidak pernah dikirim ke browser |
| Viewer | Schema, tabel, kolom, constraint, indeks, dan data dengan pagination. Nilai `NULL`, bigint besar, desimal, dan biner ditampilkan tanpa kehilangan makna |
| Migrasi | Editor SQL, draft dengan revisi, eksekusi transaksional, riwayat dengan versi, pelaksana, waktu, dan status; hasil yang tidak pasti direkonsiliasi, bukan diulang otomatis |
| Bonus: SQLite | Viewer, migrasi, dan kontrol akses juga berjalan untuk SQLite |
| Bonus: EXPLAIN | Analisis rencana eksekusi satu statement (mode estimasi, tanpa `ANALYZE`) untuk PostgreSQL dan SQLite |

## Teknologi

| Bagian | Teknologi |
|---|---|
| Backend | Go 1.27.2, `net/http` standard library, `html/template` untuk halaman |
| Frontend | Halaman multipage dirender server; TypeScript, Vite, dan widget React untuk bagian interaktif; CodeMirror untuk editor SQL |
| Database | PostgreSQL 17.11 (metadata aplikasi dan target), SQLite (target tambahan) |
| Driver | pgx v5, go-sqlite3 (butuh CGO), pg_query_go untuk mem-parse SQL PostgreSQL |
| Runtime | Docker Compose; image dibangun untuk Linux amd64 |

## Prasyarat

- **Docker Desktop** (atau Docker Engine dengan Compose v2).
- **Shell POSIX** untuk menjalankan script di `scripts/`. Di Windows gunakan **Git Bash**.
- Go 1.27.2 (dengan compiler C untuk CGO) dan Node.js 24.22.0 hanya diperlukan untuk mode development di host. Test dan build berjalan di container.

## Menjalankan aplikasi

Dari root repository:

```sh
sh scripts/init-env.sh      # buat .env dan isi secret lokal secara acak
sh scripts/dev-up.sh        # nyalakan PostgreSQL metadata dan target
sh scripts/app-up.sh seed   # build image, migrasi metadata, isi data demo, jalankan aplikasi
```

Buka http://localhost:8080.

- `init-env.sh` hanya mengisi kunci yang masih kosong, jadi aman dijalankan ulang dan tidak menimpa nilai yang sudah Anda isi.
- `.env` berisi secret lokal: jangan di-commit dan jangan dibagikan. Setiap anggota membuat `.env` sendiri.
- Tanpa argumen `seed`, `app-up.sh` hanya build, migrasi, dan menjalankan aplikasi.

### Akun dan data demo

`app-up.sh seed` membuat akun `fixture-admin` (admin platform), `editor`, `viewer`, dan `outsider`, beserta group, hak akses, dan empat koneksi demo. Password semua akun demo adalah nilai `FIXTURE_PASSWORD` di `.env` Anda.

`seed` juga membuat database SQLite demo (`demo.sqlite`) dari `tests/fixtures/sqlite/demo.sql`, tetapi hanya bila file itu belum ada. Data dan riwayat migrasi di dalamnya tetap tersimpan saat `seed` dijalankan ulang. Untuk memulai dari awal, hapus volume dengan `sh scripts/dev-up.sh --profile app down -v`.

Menjalankan `seed` ulang mencabut session akun demo, jadi login kembali setelahnya.

Sebelum migrasi dapat dijalankan pada sebuah koneksi, admin harus **menguji koneksi itu terlebih dahulu** di halaman Koneksi. Sampai diuji, halaman migrasi menjawab `TARGET_NOT_READY`.

Di dalam Compose, aplikasi mencapai PostgreSQL target sebagai `target-postgres` port `5432`. Gunakan alamat ini saat mendaftarkan koneksi baru lewat halaman Koneksi.

### Menghentikan

```sh
sh scripts/dev-up.sh --profile app down      # hentikan semua container, data tetap ada
sh scripts/dev-up.sh --profile app down -v   # hentikan dan HAPUS seluruh data
```

## Development di host

Untuk mengubah kode tanpa membangun ulang image. Jalankan database dengan `scripts/dev-up.sh`, lalu:

```sh
# backend: build lalu jalankan dari root repository
(cd backend && go build -o bin/ ./cmd/server ./cmd/migrate ./cmd/seed)
set -a; . ./.env; set +a
./backend/bin/migrate up
./backend/bin/seed fixtures   # opsional: data demo
./backend/bin/server

# frontend: di terminal lain, build ulang otomatis setiap kali file berubah
cd web && npm ci && npm run dev
```

Server membaca daftar asset hasil build saat start. Restart server setelah build frontend pertama kali dan setelah menambah entry halaman baru.

## Test

```sh
sh scripts/test-backend.sh   # gofmt, go vet, dan seluruh test Go (Go 1.27.2 di container)
sh scripts/test-web.sh       # npm ci, typecheck, test, dan build frontend (Node 24.22.0 di container)
```

Integration test backend memakai PostgreSQL asli dari `scripts/dev-up.sh` dan URL `TEST_*_DATABASE_URL` di `.env` (sudah diisi oleh `init-env.sh`). Tanpa URL tersebut, test integration dilewati dengan pesan. Argumen tambahan diteruskan ke `go test`, misalnya `sh scripts/test-backend.sh -run Fingerprint ./internal/database/`.

## Arsitektur singkat

- **Satu backend, satu origin.** Go menyajikan halaman HTML (`backend/internal/webui`), API JSON di `/api/v1`, dan asset hasil build Vite.
- **Multipage, bukan SPA.**
  - Setiap URL dirender di server dan dapat dibuka langsung atau di-refresh.
  - Perpindahan halaman memakai link biasa.
  - JavaScript hanya dipakai untuk bagian interaktif (form koneksi, editor migrasi, panel EXPLAIN), yang dipasang pada elemen `data-widget`.
  - URL yang tidak dikenal menjawab 404.
- **Dua jenis database.**
  - Database *metadata* menyimpan pengguna, session, hak akses, registry, draft, dan riwayat.
  - Database *target* adalah database yang dikelola pengguna.
  - Metadata tidak dapat didaftarkan sebagai target.
- **Akses dicek di backend** pada setiap request, baik halaman maupun API; tombol di UI hanya kenyamanan.
- **Pembacaan target dibatasi.** Pada PostgreSQL, pembacaan data oleh viewer dan EXPLAIN berjalan sebagai role `platform_editor` di dalam transaksi read-only. Namespace internal `_platform` disembunyikan dari pengguna.

## Struktur repository

```text
backend/
  cmd/server, cmd/migrate, cmd/seed   aplikasi, migrasi metadata, bootstrap admin/data demo
  internal/auth, access               session, login, CSRF, kebijakan akses
  internal/connections                registry koneksi dan enkripsi kredensial
  internal/database                   kontrak target, adapter postgres/ dan sqlite/
  internal/viewer, explain            pembacaan schema/data dan rencana eksekusi
  internal/migrations                 draft, eksekusi transaksional, riwayat, rekonsiliasi
  internal/webui                      halaman HTML dan template
  migrations/                         migrasi schema metadata
web/src/
  api/                                client HTTP dan tipe DTO
  app/, pages/                        layout, logout, pemasangan widget, entry per halaman
  features/                           widget per fitur (connections, migrations, explain)
infra/docker/, infra/postgres/        Dockerfile, Compose, role target
tests/fixtures/                       payload API, fixture PostgreSQL dan SQLite bersama
scripts/                              setup, menjalankan aplikasi, dan test
```

## Troubleshooting

| Gejala | Penyebab dan solusi |
|---|---|
| Test atau aplikasi gagal dengan `password authentication failed` | Volume database dibuat dengan password lama; PostgreSQL hanya membaca password saat volume pertama kali dibuat. Samakan `.env` dengan password lama, atau reset dengan `sh scripts/dev-up.sh down -v` (menghapus data) lalu `sh scripts/dev-up.sh` |
| Perintah Docker di Git Bash gagal dengan path seperti `C:/Program Files/Git/app/...` | Git Bash mengubah path Linux menjadi path Windows. Gunakan script di `scripts/` atau awali perintah dengan `MSYS_NO_PATHCONV=1` |
| Port 8080, 15432, atau 15433 sudah dipakai | Hentikan proses atau container lain yang memakai port tersebut |
| Halaman migrasi menampilkan `TARGET_NOT_READY` | Koneksi belum diuji. Admin menekan Uji pada halaman Koneksi |
| Server berhenti dengan `frontend build manifest is unavailable` | Frontend belum di-build. Jalankan `npm run build` (atau `npm run dev`) di `web/`, atau gunakan `scripts/app-up.sh` |

## Batasan saat ini

Perbarui bagian ini ketika fiturnya selesai.

- Halaman login dan halaman administrasi user/group/grant belum tersedia di UI.
- Fitur bonus schema diff belum tersedia (capability `schema_diff` bernilai `false`).
- CI belum dikonfigurasi; jalankan kedua script test secara lokal.
- Koneksi demo "SQLite desired" (`demo_desired.sqlite`, pembanding untuk schema diff) belum memiliki fixture, jadi berstatus tidak dapat dihubungi.
- Pada mode development di host, `seed fixtures` tidak membuat `demo.sqlite`; buat file itu sendiri di `SQLITE_TARGET_DIR` atau gunakan `scripts/app-up.sh seed`.
- EXPLAIN hanya mode estimasi; `EXPLAIN ANALYZE` tidak diimplementasikan.

## Anggota

| Anggota | Tanggung jawab utama |
|---|---|
| Neswa | Autentikasi, session, kebijakan akses, administrasi user/group/grant, renderer halaman |
| Samson | Registry koneksi, adapter PostgreSQL/SQLite, viewer, EXPLAIN, layout dan build frontend, Docker/script |
| Argha | Draft dan eksekusi migrasi, riwayat dan rekonsiliasi, editor SQL, schema diff |
