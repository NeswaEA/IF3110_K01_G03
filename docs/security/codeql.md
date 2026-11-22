# Laporan CodeQL

## Cakupan

Workflow [`codeql.yml`](../../.github/workflows/codeql.yml) berjalan pada setiap push, pull request, jadwal mingguan (Senin 09:17 WIB), dan manual. Suite query `security-extended`, `github/codeql-action` v4.39.3.

| Bahasa | Build | Yang dianalisis |
|---|---|---|
| `go` | Manual, `go build ./...` dengan `CGO_ENABLED=1` (SQLite dan `pg_query` butuh CGO) | `backend/`: keempat biner dan seluruh paket `internal/` |
| `javascript-typescript` | `none` | `web/` (SPA), `tests/e2e` (Playwright), `tests/load` (k6), script Node di `scripts/` |
| `actions` | `none` | `.github/workflows/*.yml` |

## Hasil

7 alert, semuanya di Go. `javascript-typescript` dan `actions` tidak menghasilkan alert.

Enam alert `go/sql-injection` menandai tempat SQL dari pengguna sampai ke database. Mengirim SQL ke database target memang fungsi inti platform ini (viewer, EXPLAIN, lint migration), jadi alirannya tidak bisa dihilangkan. Yang diperiksa adalah apakah ada pelindung di setiap jalur. Tidak ada temuan yang dapat dieksploitasi.

| # | Rule | Severity | Lokasi | Dampak bila valid | Keputusan | Alasan |
|---|---|---|---|---|---|---|
| 1 | `go/sql-injection` | High | `backend/internal/database/postgres/reader.go:173` | Viewer data membaca tabel lain atau menjalankan SQL sembarang | False positive | Schema dan tabel dari URL dicocokkan dulu ke katalog (`pg_namespace`/`pg_class`, sebagai parameter). Nama kolom diambil dari katalog. Semua identifier di-quote `QuoteIdent` (tanda kutip digandakan). `LIMIT`/`OFFSET` dikirim sebagai `$1`/`$2`. Query berjalan di transaksi `READ ONLY` sebagai role editor target |
| 2 | `go/sql-injection` | High | `backend/internal/database/sqlite/reader.go:83` | Sama dengan #1, pada SQLite | False positive | Tabel harus ada di `sqlite_schema` (dicek dengan parameter `?`). Tabel internal `sqlite_*` ditolak. Identifier di-quote. `LIMIT`/`OFFSET` dikirim sebagai parameter |
| 3 | `go/sql-injection` | High | `backend/internal/explain/sqlite_policy.go:89` | SQL pengguna dijalankan saat klasifikasi EXPLAIN | Risiko diterima | Statement hanya di-`Prepare` lalu ditutup, tidak pernah di-*step*, jadi tidak dieksekusi. Authorizer mencatat operasi dan menolak tabel `_platform_*`. Endpoint butuh akses `edit` ke database |
| 4 | `go/sql-injection` | High | `backend/internal/database/postgres/explain.go:29` | SQL pengguna dijalankan saat meminta rencana kueri | Risiko diterima (fitur EXPLAIN) | `EXPLAIN (FORMAT JSON)` tanpa `ANALYZE`, sehingga statement tidak dijalankan; `ANALYZE` ditolak service. Paket `explain` memastikan tepat satu statement yang bisa direncanakan. Transaksi `READ ONLY` sebagai role editor target, selalu di-rollback, dengan `statement_timeout`. Butuh akses `edit` |
| 5 | `go/sql-injection` | High | `backend/internal/database/sqlite/explain.go:78` | Sama dengan #4, pada SQLite | Risiko diterima (fitur EXPLAIN) | `EXPLAIN QUERY PLAN` tidak menjalankan statement. Statement tunggal dicek oleh paket `explain`, authorizer menolak `_platform_*`, dan endpoint butuh akses `edit` |
| 6 | `go/log-injection` | Medium | `backend/internal/platform/http.go:144` | Penyerang menyisipkan baris log palsu | Diperbaiki | Tidak dapat dieksploitasi: `slog.JSONHandler` meng-escape baris baru dan kutip, dan di cabang 500 `response.Code` selalu `INTERNAL_ERROR` dari daftar tetap `errorStatuses`. CodeQL tetap melihat aliran dari error ke log, jadi log sekarang menulis konstanta `INTERNAL_ERROR` dan aliran itu hilang. Commit `fix(platform): log the fixed internal error code` |
| 7 | `go/sql-injection` | High | `backend/internal/migrations/lint_sqlite.go:150` | SQL migration dijalankan saat lint | Risiko diterima (fitur lint) | Lint hanya `Prepare` lalu `Close` di bawah `migrationAuthorizer`. Token `_platform` ditolak sebelum prepare. Membuat dan mengubah migration butuh akses `edit`. SQL migration baru dijalankan worker setelah approval semua group yang wajib |

Commit perbaikan: `fix(platform): log the fixed internal error code` (#6).

## Tindak lanjut di GitHub

Enam alert di-dismiss di tab *Security → Code scanning* dengan alasan dari tabel di atas:

- #1, #2: *False positive*;
- #3, #4, #5, #7: *Won't fix*, karena eksekusi atau prepare SQL pengguna adalah fitur yang dijaga oleh pelindung di atas.

#6 tidak di-dismiss: scan ulang setelah commit perbaikan masuk ke `main` menutup alert itu otomatis dengan status *Fixed*. Scan berikutnya tidak membuka ulang alert yang sudah di-dismiss selama kodenya tidak berubah. Alert baru di lokasi lain tetap muncul dan wajib ditriase dengan format yang sama.
