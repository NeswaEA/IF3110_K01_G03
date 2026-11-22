# Contoh log JSON

Output nyata dari satu sesi lewat API pada 17 November 2026, terhadap database `PostgreSQL demo` (`30000000-0000-4000-8000-000000000001`). Satu login gagal, admin menguji koneksi dan me-reset password `outsider`, lalu `editor` mengajukan tiga migration. `viewer` dan `lead` meng-approve dua di antaranya dan `lead` menolak yang ketiga. Migration pertama sukses, migration kedua gagal karena primary key ganda. Log diambil dari stdout API dan kedua worker:

```sh
sh scripts/app-up.sh seed
docker compose --env-file .env -f infra/docker/compose.yml --profile app logs --no-log-prefix backend worker | grep '^{"time"'
```

Sesi ini menghasilkan 123 baris log. Semuanya JSON valid dan memuat `time`, `level`, `msg`, `event`, dan `service`. Password, password baru, token CSRF, cookie session, DSN, dan literal SQL pengguna tidak muncul di baris mana pun.

## Atribut

| Kelompok | Kunci |
|---|---|
| Umum | `time`, `level`, `msg`, `event`, `service`, `request_id` (request HTTP), `actor_id` (pengguna login), `trace_id`/`span_id` (span aktif), `database_id`, `status` |
| Migration | `migration_id`, `migration_revision`, `execution_id`, `job_id` (= `execution_id`), `change_request_id`, `attempt`, `batch`, `direction` |
| SQL | `db.pool`, `db.operation`, `db.query`, `db.params`, `duration_ms`, `rows`, `sqlstate`, `error_code` |
| Audit (`event=audit`) | `action`, `actor_id`, `actor_name`, `database_id`, `resource_type`, `resource_id`, `details` |

`request_id`, `actor_id`, `trace_id`, dan `span_id` diisi logger dari context, sehingga modul tidak perlu menuliskannya sendiri.

## Redaksi

- Kunci yang cocok dengan `(?i)password|secret|token|cookie|csrf|dsn|authorization|session` diganti `"[REDACTED]"`, termasuk di dalam group bersarang.
- `db.params` hanya berisi tipe parameter, tidak pernah nilainya.
- SQL yang ditulis pengguna dinormalisasi sebelum dicatat: literal menjadi `$n`. Contohnya, `'catatan rahasia'` di bawah tercatat sebagai `$2`.
- Log `http.request` hanya memuat pola route, tidak pernah path mentah, query string, atau body.
- Login gagal tidak mencatat username yang diketik, karena orang kadang menempelkan password ke kolom itu.
- `details` audit membuang kunci SQL (`sql`, `up_sql`, `down_sql`, `query`, `statement`); revisi dicatat lewat `content_hash`.
- Panic di handler dicatat sebagai `http.panic` dengan tipe dan stack trace, tanpa nilai panic-nya.

## Login dan request HTTP

Login gagal (WARN, tanpa username), login berhasil (dengan `actor_id`), dan satu request `execute` yang dijawab 202:

```json
{"time":"2026-11-17T10:44:31.915348678Z","level":"WARN","msg":"login rejected","service":"dbplatform-api","event":"auth.login","status":"failed","error_code":"UNAUTHENTICATED","request_id":"QGVWX45QBCSKVPL77JWUP5QHHO","trace_id":"656edb3ad0c9922b6ed8419ebd3a4c09","span_id":"8c77d01d5f7bf7d2"}
{"time":"2026-11-17T10:44:31.972597353Z","level":"INFO","msg":"login succeeded","service":"dbplatform-api","event":"auth.login","status":"ok","request_id":"MNCNTOYVE67Y2CG63N2GEWMP4E","actor_id":"10000000-0000-4000-8000-000000000001","trace_id":"565890d8e761d70443a53c8d0e5a1184","span_id":"6d81680da0144895"}
{"time":"2026-11-17T10:44:32.480563409Z","level":"INFO","msg":"HTTP request","service":"dbplatform-api","event":"http.request","method":"POST","route":"POST /api/v1/databases/{id}/change-requests/{rid}/execute","status":202,"duration_ms":18.984,"request_id":"LPGF66KMTCJ3V25HOEYRIGOXE2","actor_id":"10000000-0000-4000-8000-000000000003","trace_id":"2667a0953226e2c42a27afb587e39a1e","span_id":"83d1ebf6e6559f57"}
```

## Review

Approval pertama (`viewer`) masih menyisakan group lain, jadi status tetap `pending_review`. Satu penolakan disertai komentar; isi komentar tidak masuk log. Setiap keputusan juga menghasilkan baris audit di request yang sama:

```json
{"time":"2026-11-17T10:44:32.408376582Z","level":"INFO","msg":"change request approved","service":"dbplatform-api","event":"review.approved","database_id":"30000000-0000-4000-8000-000000000001","change_request_id":"01a1e91b-bfa8-7135-8d07-14bb29beabe3","migration_id":"01a1e91b-bf95-7c5d-a0fa-c648e36c41d6","migration_revision":1,"status":"pending_review","request_id":"JEAP3K5AHPD4UCUYRBE2OOW3HZ","actor_id":"10000000-0000-4000-8000-000000000002","trace_id":"5aa6dd9035a5bccc94615b53ea6a1503","span_id":"5b1f7cac3053556a"}
{"time":"2026-11-17T10:44:32.408497219Z","level":"INFO","msg":"audit","service":"dbplatform-api","event":"audit","action":"request.approved","actor_id":"10000000-0000-4000-8000-000000000002","actor_name":"viewer","database_id":"30000000-0000-4000-8000-000000000001","resource_type":"change_request","resource_id":"01a1e91b-bfa8-7135-8d07-14bb29beabe3","details":{"action":"apply","content_hash":"sha256:d8f5b02d8ab0a1e9f4bbc56ac72315fb373e43fac4d5ad30cb8e06bba167a454","group_ids":["20000000-0000-4000-8000-000000000001"],"migration_id":"01a1e91b-bf95-7c5d-a0fa-c648e36c41d6","revision":1,"status":"pending_review"},"request_id":"JEAP3K5AHPD4UCUYRBE2OOW3HZ","trace_id":"5aa6dd9035a5bccc94615b53ea6a1503","span_id":"5b1f7cac3053556a"}
{"time":"2026-11-17T10:44:32.456360762Z","level":"INFO","msg":"change request rejected","service":"dbplatform-api","event":"review.rejected","database_id":"30000000-0000-4000-8000-000000000001","change_request_id":"01a1e91b-bfde-715f-b808-67f47792a4c6","migration_id":"01a1e91b-bfd1-794c-bb5a-070683c9f89f","migration_revision":1,"status":"rejected","request_id":"7NKYPGRINLSV6NFFJTTNPAJ2YD","actor_id":"10000000-0000-4000-8000-000000000005","trace_id":"042f73335d4a84aae98913d278b806d0","span_id":"6534188ddd0f83f4"}
{"time":"2026-11-17T10:44:32.456437179Z","level":"INFO","msg":"audit","service":"dbplatform-api","event":"audit","action":"request.rejected","actor_id":"10000000-0000-4000-8000-000000000005","actor_name":"lead","database_id":"30000000-0000-4000-8000-000000000001","resource_type":"change_request","resource_id":"01a1e91b-bfde-715f-b808-67f47792a4c6","details":{"action":"apply","content_hash":"sha256:ec42cd2b63f60d5e57e53ae9b1137c1dd2e21624f6b60f6168304e0be1571db6","group_ids":["20000000-0000-4000-8000-000000000002"],"migration_id":"01a1e91b-bfd1-794c-bb5a-070683c9f89f","revision":1,"status":"rejected"},"request_id":"7NKYPGRINLSV6NFFJTTNPAJ2YD","trace_id":"042f73335d4a84aae98913d278b806d0","span_id":"6534188ddd0f83f4"}
```

## Eksekusi sukses

Dari API ke worker dalam satu trace (`2667a0953226e2c42a27afb587e39a1e`). `outbox.published` ditulis relay di trace sendiri:

```json
{"time":"2026-11-17T10:44:32.479973279Z","level":"INFO","msg":"execution accepted","service":"dbplatform-api","event":"execution.enqueued","execution_id":"01a1e91b-c058-72df-844b-c21de600ed6d","job_id":"01a1e91b-c058-72df-844b-c21de600ed6d","database_id":"30000000-0000-4000-8000-000000000001","change_request_id":"01a1e91b-bfa8-7135-8d07-14bb29beabe3","migration_id":"01a1e91b-bf95-7c5d-a0fa-c648e36c41d6","migration_revision":1,"status":"queued","request_id":"LPGF66KMTCJ3V25HOEYRIGOXE2","actor_id":"10000000-0000-4000-8000-000000000003","trace_id":"2667a0953226e2c42a27afb587e39a1e","span_id":"83d1ebf6e6559f57"}
{"time":"2026-11-17T10:44:32.482549754Z","level":"INFO","msg":"job published","service":"dbplatform-api","event":"outbox.published","execution_id":"01a1e91b-c058-72df-844b-c21de600ed6d","job_id":"01a1e91b-c058-72df-844b-c21de600ed6d","database_id":"30000000-0000-4000-8000-000000000001","trace_id":"fbd27bd44ccd0face5aa85f56a7adf0a","span_id":"ceb0bf7e50c7bc9d"}
{"time":"2026-11-17T10:44:32.483005477Z","level":"INFO","msg":"job received","service":"dbplatform-worker","event":"job.received","execution_id":"01a1e91b-c058-72df-844b-c21de600ed6d","job_id":"01a1e91b-c058-72df-844b-c21de600ed6d","database_id":"30000000-0000-4000-8000-000000000001","delivery":1,"trace_id":"2667a0953226e2c42a27afb587e39a1e","span_id":"53c23adb70dc570e"}
{"time":"2026-11-17T10:44:32.522551809Z","level":"INFO","msg":"lock.acquired","service":"dbplatform-worker","event":"lock.acquired","execution_id":"01a1e91b-c058-72df-844b-c21de600ed6d","job_id":"01a1e91b-c058-72df-844b-c21de600ed6d","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1e91b-bf95-7c5d-a0fa-c648e36c41d6","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"running","direction":"up","change_request_id":"01a1e91b-bfa8-7135-8d07-14bb29beabe3","trace_id":"2667a0953226e2c42a27afb587e39a1e","span_id":"53c23adb70dc570e"}
{"time":"2026-11-17T10:44:32.522660933Z","level":"INFO","msg":"execution.started","service":"dbplatform-worker","event":"execution.started","execution_id":"01a1e91b-c058-72df-844b-c21de600ed6d","job_id":"01a1e91b-c058-72df-844b-c21de600ed6d","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1e91b-bf95-7c5d-a0fa-c648e36c41d6","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"running","direction":"up","change_request_id":"01a1e91b-bfa8-7135-8d07-14bb29beabe3","trace_id":"2667a0953226e2c42a27afb587e39a1e","span_id":"53c23adb70dc570e"}
{"time":"2026-11-17T10:44:32.566055589Z","level":"INFO","msg":"execution.revalidated","service":"dbplatform-worker","event":"execution.revalidated","execution_id":"01a1e91b-c058-72df-844b-c21de600ed6d","job_id":"01a1e91b-c058-72df-844b-c21de600ed6d","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1e91b-bf95-7c5d-a0fa-c648e36c41d6","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"running","direction":"up","change_request_id":"01a1e91b-bfa8-7135-8d07-14bb29beabe3","trace_id":"2667a0953226e2c42a27afb587e39a1e","span_id":"95dac85dd1089170"}
{"time":"2026-11-17T10:44:32.538186923Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.tx.begin","db.pool":"target","db.operation":"BEGIN","db.query":"BEGIN","db.params":[],"duration_ms":0.253,"database_id":"30000000-0000-4000-8000-000000000001","rows":0,"status":"ok","execution_id":"01a1e91b-c058-72df-844b-c21de600ed6d","job_id":"01a1e91b-c058-72df-844b-c21de600ed6d","actor_id":"10000000-0000-4000-8000-000000000003","migration_id":"01a1e91b-bf95-7c5d-a0fa-c648e36c41d6","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e91b-bfa8-7135-8d07-14bb29beabe3","trace_id":"2667a0953226e2c42a27afb587e39a1e","span_id":"e0350d83b2aac198"}
{"time":"2026-11-17T10:44:32.569586202Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.query","db.pool":"target","db.operation":"INSERT","db.query":"INSERT INTO log_demo_1794912272 VALUES ($1, $2)","db.params":[],"duration_ms":0.31,"database_id":"30000000-0000-4000-8000-000000000001","rows":1,"status":"ok","execution_id":"01a1e91b-c058-72df-844b-c21de600ed6d","job_id":"01a1e91b-c058-72df-844b-c21de600ed6d","actor_id":"10000000-0000-4000-8000-000000000003","migration_id":"01a1e91b-bf95-7c5d-a0fa-c648e36c41d6","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e91b-bfa8-7135-8d07-14bb29beabe3","trace_id":"2667a0953226e2c42a27afb587e39a1e","span_id":"e82b02722583c00d"}
{"time":"2026-11-17T10:44:32.570895599Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.tx.commit","db.pool":"target","db.operation":"COMMIT","db.query":"COMMIT","db.params":[],"duration_ms":0.432,"database_id":"30000000-0000-4000-8000-000000000001","rows":0,"status":"ok","execution_id":"01a1e91b-c058-72df-844b-c21de600ed6d","job_id":"01a1e91b-c058-72df-844b-c21de600ed6d","actor_id":"10000000-0000-4000-8000-000000000003","migration_id":"01a1e91b-bf95-7c5d-a0fa-c648e36c41d6","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e91b-bfa8-7135-8d07-14bb29beabe3","trace_id":"2667a0953226e2c42a27afb587e39a1e","span_id":"f981f9f863a5993e"}
{"time":"2026-11-17T10:44:32.576810972Z","level":"INFO","msg":"audit","service":"dbplatform-worker","event":"audit","action":"history.recorded","actor_id":"10000000-0000-4000-8000-000000000003","actor_name":"editor","database_id":"30000000-0000-4000-8000-000000000001","resource_type":"execution","resource_id":"01a1e91b-c058-72df-844b-c21de600ed6d","details":{"seq":2,"version":2},"trace_id":"2667a0953226e2c42a27afb587e39a1e","span_id":"5f42c03893c8d7e5"}
{"time":"2026-11-17T10:44:32.576594265Z","level":"INFO","msg":"execution.finished","service":"dbplatform-worker","event":"execution.finished","execution_id":"01a1e91b-c058-72df-844b-c21de600ed6d","job_id":"01a1e91b-c058-72df-844b-c21de600ed6d","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1e91b-bf95-7c5d-a0fa-c648e36c41d6","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"succeeded","direction":"up","change_request_id":"01a1e91b-bfa8-7135-8d07-14bb29beabe3","trace_id":"2667a0953226e2c42a27afb587e39a1e","span_id":"5f42c03893c8d7e5"}
{"time":"2026-11-17T10:44:32.576941351Z","level":"INFO","msg":"lock.released","service":"dbplatform-worker","event":"lock.released","execution_id":"01a1e91b-c058-72df-844b-c21de600ed6d","job_id":"01a1e91b-c058-72df-844b-c21de600ed6d","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1e91b-bf95-7c5d-a0fa-c648e36c41d6","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"running","direction":"up","change_request_id":"01a1e91b-bfa8-7135-8d07-14bb29beabe3","trace_id":"2667a0953226e2c42a27afb587e39a1e","span_id":"53c23adb70dc570e"}
```

## Eksekusi gagal

Statement kedua melanggar primary key (`sqlstate` `23505`). Seluruh transaksi target di-rollback (`db.tx.rollback`, rollback transaksi, bukan rollback migration), execution berakhir `failed` dengan `error_code`, dan tidak ada `history.recorded`:

```json
{"time":"2026-11-17T10:44:33.066883168Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.query","db.pool":"target","db.operation":"INSERT","db.query":"INSERT INTO log_demo_1794912272 VALUES ($1, $2), ($3, $4)","db.params":[],"duration_ms":0.582,"database_id":"30000000-0000-4000-8000-000000000001","sqlstate":"23505","status":"error","execution_id":"01a1e91b-c285-74df-9efa-b514e54c7db9","job_id":"01a1e91b-c285-74df-9efa-b514e54c7db9","actor_id":"10000000-0000-4000-8000-000000000003","migration_id":"01a1e91b-bfb8-771a-93e5-d4dc246d12ea","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e91b-bfc6-792f-8a02-ee1fcb27681a","trace_id":"3e4b661803abc276e7b30e15b9eacc75","span_id":"bb6342e3c04e4220"}
{"time":"2026-11-17T10:44:33.067101327Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.tx.rollback","db.pool":"target","db.operation":"ROLLBACK","db.query":"ROLLBACK","db.params":[],"duration_ms":0.073,"database_id":"30000000-0000-4000-8000-000000000001","rows":0,"status":"ok","execution_id":"01a1e91b-c285-74df-9efa-b514e54c7db9","job_id":"01a1e91b-c285-74df-9efa-b514e54c7db9","actor_id":"10000000-0000-4000-8000-000000000003","migration_id":"01a1e91b-bfb8-771a-93e5-d4dc246d12ea","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e91b-bfc6-792f-8a02-ee1fcb27681a","trace_id":"3e4b661803abc276e7b30e15b9eacc75","span_id":"81009d6c2d01aaeb"}
{"time":"2026-11-17T10:44:33.070493316Z","level":"INFO","msg":"execution.finished","service":"dbplatform-worker","event":"execution.finished","execution_id":"01a1e91b-c285-74df-9efa-b514e54c7db9","job_id":"01a1e91b-c285-74df-9efa-b514e54c7db9","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1e91b-bfb8-771a-93e5-d4dc246d12ea","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"failed","direction":"up","change_request_id":"01a1e91b-bfc6-792f-8a02-ee1fcb27681a","error_code":"SQL_EXECUTION_FAILED","trace_id":"3e4b661803abc276e7b30e15b9eacc75","span_id":"c6cbedcce0f4de22"}
```

## Outcome batch

Skenario E2E PostgreSQL mencatat commit dua baris pada batch pertama. Batch kedua gagal dan execution berakhir `partial`. `batch.committed` terbit setelah checkpoint target tercatat di metadata, termasuk ketika worker memulihkan checkpoint.

```json
{"time":"2026-11-21T10:00:22.190385667Z","level":"INFO","msg":"batch.committed","service":"dbplatform-worker","event":"batch.committed","execution_id":"01a1fd8c-bf8c-7429-8b39-0e34c9f9b789","job_id":"01a1fd8c-bf8c-7429-8b39-0e34c9f9b789","database_id":"30000000-0000-4000-8000-000000000005","migration_id":"01a1fd8c-bf6a-7614-abb4-60a2aeffea72","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000005","status":"committed","direction":"up","change_request_id":"01a1fd8c-bf7b-7970-bf2d-d6e824b958a0","batch":1,"rows":2,"trace_id":"86b730adc185af7e36f4a4c9d9160537","span_id":"ecd0c3123cff8197"}
{"time":"2026-11-21T10:00:22.191064917Z","level":"INFO","msg":"batch.failed","service":"dbplatform-worker","event":"batch.failed","execution_id":"01a1fd8c-bf8c-7429-8b39-0e34c9f9b789","job_id":"01a1fd8c-bf8c-7429-8b39-0e34c9f9b789","database_id":"30000000-0000-4000-8000-000000000005","migration_id":"01a1fd8c-bf6a-7614-abb4-60a2aeffea72","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000005","status":"failed","direction":"up","change_request_id":"01a1fd8c-bf7b-7970-bf2d-d6e824b958a0","error_code":"SQL_EXECUTION_FAILED","batch":2,"trace_id":"86b730adc185af7e36f4a4c9d9160537","span_id":"ecd0c3123cff8197"}
```

## Audit admin

Reset password hanya mencatat jenis perubahannya; password baru tidak pernah ditulis:

```json
{"time":"2026-11-17T10:44:32.060049426Z","level":"INFO","msg":"audit","service":"dbplatform-api","event":"audit","action":"user.changed","actor_id":"10000000-0000-4000-8000-000000000001","actor_name":"fixture-admin","resource_type":"user","resource_id":"10000000-0000-4000-8000-000000000004","details":{"change":"password_reset"},"request_id":"TQONANT7DVITOSKYBQAMZFS2LC","trace_id":"7a204a097d7105a90684854900fa3d77","span_id":"c875c7ed37f27616"}
```

## Batch, retry, dan recovery

Sesi kedua pada 21 November 2026, terhadap database yang sama, dengan cara pengambilan log yang sama (879 baris, semuanya JSON valid dengan atribut inti dan tanpa password, cookie, DSN, atau literal SQL). `editor` membuat tabel 120 baris lalu menjalankan migration batch (`batch_size` 20, jeda 1,5 detik) dan menghentikannya setelah batch kedua. Gangguan lain dibuat dari `psql` di container target: memegang advisory lock migration, memegang lock tabel lebih lama dari `MIGRATION_LOCK_TIMEOUT`, dan mematikan worker (`docker kill`) di tengah statement panjang.

### Batch, stop, dan resume

Setiap baris di dalam satu batch membawa atribut `batch`. Literal template (`'active'`, `1.10`) tercatat sebagai `$5` dan `$4`, sedangkan `$1`–`$3` adalah cursor, batas scope, dan ukuran batch. Stop dicatat sebagai audit, dan execution berakhir `partial` dengan `error_code` `STOPPED`:

```json
{"time":"2026-11-21T09:24:50.24378697Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.query","db.pool":"target","db.operation":"UPDATE","db.query":"WITH batch AS (SELECT id FROM public.batch_demo_1795253089 WHERE id > $1 AND id <= $2 AND status = $5 ORDER BY id LIMIT $3)\nUPDATE public.batch_demo_1795253089 AS t SET price = t.price * $4 FROM batch WHERE t.id = batch.id RETURNING t.id;","db.params":["<pgx.QueryResultFormatsByOID>","<int64>","<int64>","<int>"],"duration_ms":1.026,"database_id":"30000000-0000-4000-8000-000000000001","rows":20,"status":"ok","execution_id":"01a1fd6c-37a5-7859-8a7b-b720967fe132","job_id":"01a1fd6c-37a5-7859-8a7b-b720967fe132","actor_id":"10000000-0000-4000-8000-000000000003","migration_id":"01a1fd6c-3787-713f-b02f-6d474c3c0811","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1fd6c-3790-76b1-a978-db650d6ba56e","batch":1,"trace_id":"84d072c178e5534aadc8867fe3720718","span_id":"4144b85d89eb514d"}
{"time":"2026-11-21T09:24:51.776815301Z","level":"INFO","msg":"audit","service":"dbplatform-api","event":"audit","action":"execution.stop_requested","actor_id":"10000000-0000-4000-8000-000000000003","actor_name":"editor","database_id":"30000000-0000-4000-8000-000000000001","resource_type":"execution","resource_id":"01a1fd6c-37a5-7859-8a7b-b720967fe132","request_id":"VF6HYGDVBK3METEPVIDHYA3B43","trace_id":"47cf1669aced64bfbd2b117571d18d84","span_id":"73caf31d625d7771"}
{"time":"2026-11-21T09:24:51.860715359Z","level":"INFO","msg":"execution.finished","service":"dbplatform-worker","event":"execution.finished","execution_id":"01a1fd6c-37a5-7859-8a7b-b720967fe132","job_id":"01a1fd6c-37a5-7859-8a7b-b720967fe132","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1fd6c-3787-713f-b02f-6d474c3c0811","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"partial","direction":"up","change_request_id":"01a1fd6c-3790-76b1-a978-db650d6ba56e","error_code":"STOPPED","trace_id":"84d072c178e5534aadc8867fe3720718","span_id":"7d895ed789934ccf"}
```

Resume membuat execution baru (`action` `resume` di audit) yang melanjutkan dari checkpoint dan menutup chain:

```json
{"time":"2026-11-21T09:25:01.907822598Z","level":"INFO","msg":"audit","service":"dbplatform-api","event":"audit","action":"execution.enqueued","actor_id":"10000000-0000-4000-8000-000000000003","actor_name":"editor","database_id":"30000000-0000-4000-8000-000000000001","resource_type":"execution","resource_id":"01a1fd6c-6551-7d18-8d03-28e6bcacbd42","details":{"action":"resume","migration_id":"01a1fd6c-3787-713f-b02f-6d474c3c0811","revision":1,"status":"queued"},"request_id":"HBSXOPD2ELFTMKCEMUE4RWGCJA","trace_id":"0d9fdb9d27f9b6034aec2519ff90a4df","span_id":"9c5a11bcea432cb9"}
{"time":"2026-11-21T09:25:07.95719818Z","level":"INFO","msg":"execution.finished","service":"dbplatform-worker","event":"execution.finished","execution_id":"01a1fd6c-6551-7d18-8d03-28e6bcacbd42","job_id":"01a1fd6c-6551-7d18-8d03-28e6bcacbd42","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1fd6c-3787-713f-b02f-6d474c3c0811","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"succeeded","direction":"up","trace_id":"0d9fdb9d27f9b6034aec2519ff90a4df","span_id":"79e5b999386b2da7"}
{"time":"2026-11-21T09:25:07.957359049Z","level":"INFO","msg":"audit","service":"dbplatform-worker","event":"audit","action":"history.recorded","actor_id":"10000000-0000-4000-8000-000000000003","actor_name":"editor","database_id":"30000000-0000-4000-8000-000000000001","resource_type":"execution","resource_id":"01a1fd6c-6551-7d18-8d03-28e6bcacbd42","details":{"seq":4,"version":4},"trace_id":"0d9fdb9d27f9b6034aec2519ff90a4df","span_id":"79e5b999386b2da7"}
```

### Execution ditahan

Migration lain di database yang sama sudah antre sebelum stop. Saat worker mengambilnya, database sedang diblokir oleh execution `partial`, jadi execution itu ditahan (`held_reason` `partial` di API). Setelah resume selesai, execution ini dilepas dan berakhir `succeeded`:

```json
{"time":"2026-11-21T09:25:01.782468097Z","level":"INFO","msg":"execution.held","service":"dbplatform-worker","event":"execution.held","execution_id":"01a1fd6c-3dba-73b5-a500-db40f4591e57","job_id":"01a1fd6c-3dba-73b5-a500-db40f4591e57","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1fd6c-37aa-725b-b80e-395bbbfc8975","migration_revision":1,"attempt":0,"actor_id":"10000000-0000-4000-8000-000000000003","status":"held","direction":"up","change_request_id":"01a1fd6c-37af-7579-b9bf-95a02ab51898","trace_id":"743122698dc2f61fd108da83feabd81a","span_id":"2b13c4620be27c1a"}
{"time":"2026-11-21T09:25:07.999901037Z","level":"INFO","msg":"execution.finished","service":"dbplatform-worker","event":"execution.finished","execution_id":"01a1fd6c-3dba-73b5-a500-db40f4591e57","job_id":"01a1fd6c-3dba-73b5-a500-db40f4591e57","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1fd6c-37aa-725b-b80e-395bbbfc8975","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"succeeded","direction":"up","change_request_id":"01a1fd6c-37af-7579-b9bf-95a02ab51898","trace_id":"0d9fdb9d27f9b6034aec2519ff90a4df","span_id":"509dac9913e3bcda"}
```

### Lock sedang dipakai

Sesi `psql` memegang `pg_advisory_lock(hashtextextended('if3110.migration', 0))` selama 6 detik. Worker mencatat `lock.busy`, mengembalikan job ke antrean dengan backoff, lalu berhasil setelah lock dilepas:

```json
{"time":"2026-11-21T09:25:09.517745058Z","level":"INFO","msg":"lock.busy","service":"dbplatform-worker","event":"lock.busy","execution_id":"01a1fd6c-8308-7de3-81a3-7bdc92d01f82","job_id":"01a1fd6c-8308-7de3-81a3-7bdc92d01f82","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1fd6c-82ef-787c-bb28-702879aaf04d","migration_revision":1,"attempt":0,"actor_id":"10000000-0000-4000-8000-000000000003","status":"queued","direction":"up","change_request_id":"01a1fd6c-82f7-7924-849d-7ddfe1238c3b","trace_id":"b6cf2da32ba0c644d66f631b7977aa65","span_id":"b1c57ee05794a268"}
{"time":"2026-11-21T09:25:21.777329856Z","level":"INFO","msg":"lock.acquired","service":"dbplatform-worker","event":"lock.acquired","execution_id":"01a1fd6c-8308-7de3-81a3-7bdc92d01f82","job_id":"01a1fd6c-8308-7de3-81a3-7bdc92d01f82","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1fd6c-82ef-787c-bb28-702879aaf04d","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"running","direction":"up","change_request_id":"01a1fd6c-82f7-7924-849d-7ddfe1238c3b","trace_id":"b6cf2da32ba0c644d66f631b7977aa65","span_id":"9ea7af51f93bad3b"}
{"time":"2026-11-21T09:25:21.789821483Z","level":"INFO","msg":"execution.finished","service":"dbplatform-worker","event":"execution.finished","execution_id":"01a1fd6c-8308-7de3-81a3-7bdc92d01f82","job_id":"01a1fd6c-8308-7de3-81a3-7bdc92d01f82","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1fd6c-82ef-787c-bb28-702879aaf04d","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"succeeded","direction":"up","change_request_id":"01a1fd6c-82f7-7924-849d-7ddfe1238c3b","trace_id":"b6cf2da32ba0c644d66f631b7977aa65","span_id":"fd8d6ae0b9b4dc67"}
```

### Retry setelah error transient

Sesi `psql` memegang `ACCESS EXCLUSIVE` pada tabel selama 8 detik. `ALTER TABLE` berhenti setelah `lock_timeout` 5 detik dengan `sqlstate` `55P03`, yang tergolong transient. Execution dijadwalkan ulang (`LOCK_TIMEOUT`), dan percobaan kedua berhasil:

```json
{"time":"2026-11-21T09:25:28.194153544Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.query","db.pool":"target","db.operation":"ALTER","db.query":"ALTER TABLE batch_demo_1795253089 ADD COLUMN tag text","db.params":[],"duration_ms":5000.685,"database_id":"30000000-0000-4000-8000-000000000001","sqlstate":"55P03","status":"error","execution_id":"01a1fd6c-b86e-798b-8466-043a4838e274","job_id":"01a1fd6c-b86e-798b-8466-043a4838e274","actor_id":"10000000-0000-4000-8000-000000000003","migration_id":"01a1fd6c-b858-7e71-a849-e4bb6e3b7afc","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1fd6c-b860-7d2a-9158-20024fe9b89e","trace_id":"e3a126ed97f831c2471f805bb8191843","span_id":"e9a02441424da372"}
{"time":"2026-11-21T09:25:28.205327135Z","level":"INFO","msg":"execution.retry_scheduled","service":"dbplatform-worker","event":"execution.retry_scheduled","execution_id":"01a1fd6c-b86e-798b-8466-043a4838e274","job_id":"01a1fd6c-b86e-798b-8466-043a4838e274","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1fd6c-b858-7e71-a849-e4bb6e3b7afc","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"queued","direction":"up","change_request_id":"01a1fd6c-b860-7d2a-9158-20024fe9b89e","error_code":"LOCK_TIMEOUT","trace_id":"e3a126ed97f831c2471f805bb8191843","span_id":"cc350453dee7757c"}
{"time":"2026-11-21T09:25:28.80824053Z","level":"INFO","msg":"job received","service":"dbplatform-worker","event":"job.received","execution_id":"01a1fd6c-b86e-798b-8466-043a4838e274","job_id":"01a1fd6c-b86e-798b-8466-043a4838e274","database_id":"30000000-0000-4000-8000-000000000001","delivery":2,"trace_id":"e3a126ed97f831c2471f805bb8191843","span_id":"a7864c5d5d1927d0"}
{"time":"2026-11-21T09:25:30.245242384Z","level":"INFO","msg":"execution.finished","service":"dbplatform-worker","event":"execution.finished","execution_id":"01a1fd6c-b86e-798b-8466-043a4838e274","job_id":"01a1fd6c-b86e-798b-8466-043a4838e274","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1fd6c-b858-7e71-a849-e4bb6e3b7afc","migration_revision":1,"attempt":2,"actor_id":"10000000-0000-4000-8000-000000000003","status":"succeeded","direction":"up","change_request_id":"01a1fd6c-b860-7d2a-9158-20024fe9b89e","trace_id":"e3a126ed97f831c2471f805bb8191843","span_id":"acb5535570440c6f"}
```

### Worker mati di tengah eksekusi

Worker yang menjalankan `INSERT` besar dimatikan. Setelah `JOB_ACK_WAIT` (60 detik), NATS mengirim ulang job ke worker lain. Sebelum menjalankan SQL lagi, worker itu memeriksa bukti commit di `_platform.migration_history`. Bukti tidak ada (`found=false`), artinya transaksi pertama tidak pernah commit, jadi aman diulang:

```json
{"time":"2026-11-21T09:27:10.10574203Z","level":"INFO","msg":"job received","service":"dbplatform-worker","event":"job.received","execution_id":"01a1fd6d-6fb6-79eb-8985-962dc02a13ac","job_id":"01a1fd6d-6fb6-79eb-8985-962dc02a13ac","database_id":"30000000-0000-4000-8000-000000000001","delivery":2,"trace_id":"7541963ca9196b11ab47c55d88d94435","span_id":"605c90b56fc402aa"}
{"time":"2026-11-21T09:27:10.113591758Z","level":"INFO","msg":"commit evidence checked","service":"dbplatform-worker","event":"recovery.evidence_checked","execution_id":"01a1fd6d-6fb6-79eb-8985-962dc02a13ac","found":false,"trace_id":"7541963ca9196b11ab47c55d88d94435","span_id":"605c90b56fc402aa"}
```
