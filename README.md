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

### Session

Login yang berhasil membuat session di backend; browser hanya menerima token acak 32 byte di cookie `session` (HttpOnly, SameSite=Lax, Secure di production/HTTPS). Database hanya menyimpan hash token. Session diperiksa pada setiap request ke halaman dan API yang dilindungi.

| Pengaturan | Default | Variabel `.env` | Perilaku |
|---|---|---|---|
| Idle timeout | 30 menit | `SESSION_IDLE_TIMEOUT` | Session berakhir bila tidak ada request selama durasi ini. Setiap request yang valid memperbarui waktu aktivitas terakhir |
| Absolute expiration | 8 jam | `SESSION_ABSOLUTE_TIMEOUT` | Dihitung sejak login. Setelah lewat, pengguna harus login ulang walaupun masih aktif |
| Interval rotasi | 15 menit | `SESSION_ROTATION_INTERVAL` | Request pertama setelah interval ini mendapat token baru dengan nama cookie yang sama |
| Grace period | 30 detik | `SESSION_GRACE_PERIOD` | Token lama tetap diterima selama durasi ini setelah rotasi, untuk response yang tidak sampai atau request yang berjalan bersamaan. Setelah itu token lama ditolak |

Alasan pemilihan nilai:

- **Idle 30 menit:** cukup panjang untuk menyusun dan meninjau migrasi tanpa terputus, tetapi membatasi risiko dari browser yang ditinggalkan.
- **Absolute 8 jam:** kira-kira satu hari kerja, jadi pengguna login ulang setidaknya sekali sehari.
- **Rotasi 15 menit:** token yang bocor hanya berguna dalam jendela waktu yang pendek.
- **Grace 30 detik:** cukup untuk request paralel dan jaringan lambat, tetapi terlalu singkat untuk dipakai ulang oleh penyerang.

Aturan tambahan:

- Token juga dirotasi saat login, karena session pra-login (dipakai hanya untuk CSRF, berlaku 10 menit) dicabut dan diganti session baru. Rotasi juga terjadi pada request berikutnya setelah hak akses naik, yaitu ketika grant baru diberikan, level naik ke `edit`, pengguna ditambahkan ke group, atau data pengguna diubah oleh admin.
- Rotasi tidak memperpanjang absolute expiration. Batas ini dikunci oleh trigger database, dan grace period tidak pernah melewatinya.
- Session langsung dinonaktifkan di backend saat logout, saat level turun dari `edit` ke `view`, saat grant dicabut, saat pengguna dikeluarkan dari group, saat group dihapus, saat password direset, saat pengguna dinonaktifkan, saat hak admin dicabut, dan saat koneksi database dinonaktifkan.
- Nilai di `.env` memakai format durasi Go (misalnya `45m`, `12h`, `10s`) dan harus positif.

## Teknologi

| Bagian | Teknologi |
|---|---|
| Backend | Go 1.27.2, `net/http` standard library, `html/template` untuk halaman |
| Frontend | Halaman multipage dirender server; TypeScript, Vite, dan widget React untuk bagian interaktif; editor SQL textarea dengan seleksi dan indentasi buatan sendiri |
| Database | PostgreSQL 17.11 (metadata aplikasi dan target), SQLite (target tambahan) |
| Driver | pgx v5, go-sqlite3 (butuh CGO), pg_query_go untuk mem-parse SQL PostgreSQL |
| Runtime | Docker Compose; image dibangun mengikuti arsitektur host |

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

`seed` mengisi file SQLite `demo.sqlite` serta pembanding `demo_desired.sqlite`, dan membuat/mengisi PostgreSQL `demo_desired`. Target yang sudah memiliki objek, termasuk ledger migrasi, dilewati. File SQLite yang sudah ada tetapi masih kosong dapat diisi dalam satu transaksi. Data dan riwayat migrasi tetap tersimpan saat `seed` dijalankan ulang.

Kedua pembanding memakai schema dasar demo, lalu menambahkan `items.status`, index `items_status_idx`, dan tabel `item_tags`. Fixture dasarnya ada di `tests/fixtures/{postgres,sqlite}/demo.sql`; tambahan pembanding ada di `desired.sql` pada folder yang sama. Database lama yang sudah berisi pekerjaan tidak otomatis diubah agar mengikuti fixture baru.

Menjalankan `seed` ulang mencabut session akun demo, jadi login kembali setelahnya.

Untuk mengatur akses database, buka Administrasi → Grup → Hak akses pada grup yang dipilih. Pilih database dan level `view` atau `edit`; grant yang sudah ada dapat diubah atau dicabut. Penurunan `edit` ke `view` dan pencabutan grant mengakhiri seluruh sesi anggota grup, termasuk admin yang menjadi anggota.

Sebelum migrasi dapat dijalankan pada sebuah koneksi, admin harus **menguji koneksi itu terlebih dahulu** di halaman Koneksi. Sampai diuji, halaman migrasi menjawab `TARGET_NOT_READY`.

### Perbandingan schema

Pengguna dengan akses edit dapat membuka **Bandingkan schema** dari halaman database atau migrasi, atau langsung ke `/databases/{id}/schema-diff`. Halaman dirender Go dan dapat dibuka lewat URL/refresh biasa; JavaScript menangani interaksi di halaman tersebut tanpa router klien.

1. Admin menguji koneksi source terlebih dahulu. Pembanding cukup memiliki akses view dan tidak memerlukan ledger.
2. Pilih database pembanding sejenis dan schema, lalu tekan **Bandingkan schema**.
3. Tinjau daftar perubahan, penanda destruktif, dan warning. Kandidat yang executable dapat diedit dengan editor SQL yang sama seperti migrasi manual.
4. Isi judul dan tekan **Simpan sebagai draft**. Aplikasi membuka draft terpilih di halaman migrasi; pengguna menjalankannya sendiri lewat runner yang sama.

Schema identik atau perubahan unsupported tidak menawarkan penyimpanan kandidat kosong/parsial. Mengubah pilihan menonaktifkan penyimpanan hasil lama; generate ulang meminta konfirmasi bila SQL telah diedit. Error, konflik, atau sesi kedaluwarsa mempertahankan input. Login ulang dapat dilakukan di tab baru, lalu pengguna mengirim kembali secara manual.

### API schema diff

`POST /api/v1/databases/{id}/schema-diff` memerlukan session, CSRF, izin edit pada source, dan view pada pembanding. Source harus sudah diuji admin; pembanding tidak memerlukan ledger. Engine kedua database harus sama. Gunakan schema `public` untuk demo PostgreSQL atau `main` untuk SQLite.

Setelah login sebagai `editor`, contoh berikut dapat dijalankan dari console browser pada aplikasi lokal:

```js
const { data: { csrf_token } } = await fetch('/api/v1/auth/csrf').then(r => r.json());
const result = await fetch('/api/v1/databases/30000000-0000-4000-8000-000000000001/schema-diff', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf_token },
  body: JSON.stringify({
    desired_database_id: '30000000-0000-4000-8000-000000000003',
    schema: 'public',
  }),
}).then(r => r.json());
console.log(result);
```

Respons berisi `changes`, `warnings`, `candidate_sql`, `executable`, `expected_version`, serta fingerprint source/pembanding. Diff tidak menjalankan kandidat atau membuat draft. Kandidat non-executable selalu kosong; SQL yang akan dijalankan tetap perlu ditinjau dan disimpan sebagai draft. Draft dari halaman diff menyimpan SQL hasil edit, `expected_version`, dan `source_fingerprint` dari hasil perbandingan, sehingga runner dapat menolak target yang berubah setelahnya.

`DIFF_TIMEOUT` membatasi perbandingan (default `30s`). Capability `schema_diff` aktif untuk SQLite; PostgreSQL memerlukan probe Atlas Community v1.3.0 yang berhasil saat startup. `ATLAS_BIN` kosong memakai `atlas` dari PATH; image aplikasi sudah menyertakan binary tersebut. Atlas yang hilang menonaktifkan capability PostgreSQL dan menghasilkan `DIFF_UNAVAILABLE` bila endpoint dipanggil, sementara migrasi manual tetap tersedia. Rincian ada di [petunjuk Atlas](infra/atlas/README.md).

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
sh scripts/test-e2e.sh       # Chromium, aplikasi asli, serta database/volume test baru
```

Integration test backend memakai PostgreSQL asli dari `scripts/dev-up.sh` dan URL `TEST_*_DATABASE_URL` di `.env` (sudah diisi oleh `init-env.sh`). Tanpa URL tersebut, test integration dilewati dengan pesan. Argumen tambahan diteruskan ke `go test`, misalnya `sh scripts/test-backend.sh -run Fingerprint ./internal/database/`.

Suite browser tidak membaca `.env` lokal dan tidak memerlukan aplikasi development berjalan. Script membangun image aplikasi dan Chromium, membuat project Compose unik tanpa port host, menginisialisasi PostgreSQL/SQLite baru, serta menjalankan satu worker tanpa retry. Browser berbagi network namespace dengan aplikasi dan memakai `localhost` agar API browser seperti UUID tersedia seperti pada development. Koneksi database, auth, CSRF, Atlas PostgreSQL dan generator SQLite semuanya nyata. Network runtime tidak memiliki akses internet; download dependency hanya terjadi saat build.

Setelah selesai atau gagal, script menghapus container/network/volume milik run tersebut. Volume development tetap ada. Report JSON, screenshot, trace kegagalan, dan log server berada di `output/playwright/<run>/`. Hasil ini tidak masuk `.gitignore`: biarkan tidak terpilih saat commit. Source test, konfigurasi, dan lockfile di `tests/e2e/` tetap perlu disertakan. Suite gagal bila ada test gagal, dilewati, atau tidak ada test yang dijalankan.

Untuk memilih skenario, gunakan misalnya `sh scripts/test-e2e.sh --grep 'rolls back'`.

GitHub Actions menjalankan pemeriksaan pada setiap push, pull request, dan pemicu manual:

- **Backend:** format Go, vet, build ketiga command, dan seluruh test dengan race detector.
- **Frontend:** `npm ci`, typecheck, test, build, serta uji loader fixture SQLite dengan Node.js 24.22.0. Test halaman diff mencakup edit kandidat, perubahan pilihan, klik ganda, hasil kosong/unsupported, dan pemeliharaan input saat gagal atau sesi berakhir.
- **Metadata migrations:** CLI migrasi dijalankan dua kali pada PostgreSQL kosong, lalu test replay, checksum, rollback, concurrency, dan constraint.
- **Bonus SQLite:** adapter, registry, runner, EXPLAIN, dan integrasi server pada job tersendiri. Beberapa test juga memeriksa PostgreSQL agar perilaku kedua engine tetap konsisten.
- **Bonus Atlas:** membangun Community v1.3.0 dari commit rilis, menyiapkan database pembanding, lalu menguji config, generator, runner, dan endpoint PostgreSQL dengan session/CSRF nyata.
- **Browser workflows:** Chromium dengan aplikasi dan database asli untuk login, readiness admin, migrasi sukses/rollback, history tanpa JavaScript, View Only, CSRF, konflik, sesi berakhir, seleksi EXPLAIN, diff editable, fingerprint stale, serta reconcile setelah commit nyata.

Backend CI juga memeriksa bootstrap PostgreSQL desired dan pengulangan seed yang mempertahankan data/ledger. Uji loader SQLite dapat dijalankan dengan `node --test scripts/seed-sqlite.test.mjs` pada Node.js 24.22.0. `scripts/test-postgres-fixture.sh <container>` hanya untuk container test baru tanpa `demo_desired`, dengan folder fixture PostgreSQL terpasang pada `/fixtures`.

Setiap job backend memakai PostgreSQL 17.11 metadata dan target yang terpisah, dengan role target `platform_runner` dan `platform_editor`. Kredensial `ci_*` hanya untuk database sementara job; CI tidak membaca `.env` lokal atau membutuhkan secret repository. Hasil Go ditulis ke job summary; test case yang dilewati atau filter yang tidak menjalankan test membuat job gagal. Package tanpa test tetap diperbolehkan. Job bonus berjalan independen, sehingga kegagalan Atlas tidak membatalkan job wajib. Action dikunci dengan commit SHA dan image PostgreSQL dengan digest.

## Arsitektur proyek

- **Satu backend.** Go menyajikan halaman HTML (`backend/internal/webui`), API JSON di `/api/v1`, dan asset hasil build Vite.
- **Multipage.**
  - Setiap URL dirender di server dan dapat dibuka langsung atau di-refresh.
  - Perpindahan halaman memakai link biasa.
  - JavaScript hanya dipakai untuk bagian interaktif (form koneksi, editor migrasi, panel EXPLAIN), yang dipasang pada elemen `data-widget`.
  - URL yang tidak dikenal menjawab 404.
- **Terdiri dari dua jenis database.**
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

## Anggota

| NIM | Anggota | Tanggung jawab utama |
|---|---|---|
| 13524001 | Samuelson Dharmawan Tanuraharja | Registry koneksi, adapter PostgreSQL/SQLite, viewer, EXPLAIN, layout dan build frontend, Docker/script |
| 13524100 | Arghawisesa Dwinanda Arham | Draft dan eksekusi migrasi, riwayat dan rekonsiliasi, editor SQL, schema diff |
| 13524136 | Neswa Eka Anggara | Autentikasi, session, kebijakan akses, administrasi user/group/grant, renderer halaman |
