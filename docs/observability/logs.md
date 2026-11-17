# Contoh log JSON

Output nyata dari satu sesi lewat API pada 17 November 2026, terhadap database `PostgreSQL demo` (`30000000-0000-4000-8000-000000000001`). Satu login gagal, admin menguji koneksi dan me-reset password `outsider`, lalu `editor` mengajukan tiga migration. `viewer` dan `lead` meng-approve dua di antaranya dan `lead` menolak yang ketiga. Migration pertama sukses, migration kedua gagal karena primary key ganda. Log diambil dari stdout API dan kedua worker:

```sh
sh scripts/app-up.sh seed
docker compose --env-file .env -f infra/docker/compose.yml --profile app logs --no-log-prefix backend worker | grep '^{"time"'
```

Sesi ini menghasilkan 122 baris log. Semuanya JSON valid dan memuat `time`, `level`, `msg`, `event`, dan `service`. Password, password baru, token CSRF, cookie session, DSN, dan literal SQL pengguna tidak muncul di baris mana pun.

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
{"time":"2026-11-17T10:32:26.041624112Z","level":"WARN","msg":"login rejected","service":"dbplatform-api","event":"auth.login","status":"failed","error_code":"UNAUTHENTICATED","request_id":"7USAHE3ACK7HSELN3VDETJ3KBL","trace_id":"19125f80aa4871e1a7379b799fc63f19","span_id":"5e19a107c537c569"}
{"time":"2026-11-17T10:32:26.09708577Z","level":"INFO","msg":"login succeeded","service":"dbplatform-api","event":"auth.login","status":"ok","request_id":"QA5STX4RDNUAQ4XY77VEZS2CER","actor_id":"10000000-0000-4000-8000-000000000001","trace_id":"10a5c885543edf1b0ee06b38f32b5be2","span_id":"532486e5e691dffa"}
{"time":"2026-11-17T10:32:26.590135702Z","level":"INFO","msg":"HTTP request","service":"dbplatform-api","event":"http.request","method":"POST","route":"POST /api/v1/databases/{id}/change-requests/{rid}/execute","status":202,"duration_ms":21.394,"request_id":"PUJ3EIPITYPUZ576R4WLVHIKAW","actor_id":"10000000-0000-4000-8000-000000000003","trace_id":"210df2750aaf4629e5de23dd3c577e66","span_id":"b65ab2f6195d0efa"}
```

## Review

Approval pertama (`viewer`) masih menyisakan group lain, jadi status tetap `pending_review`. Satu penolakan disertai komentar; isi komentar tidak masuk log. Setiap keputusan juga menghasilkan baris audit di request yang sama:

```json
{"time":"2026-11-17T10:32:26.509405438Z","level":"INFO","msg":"change request approved","service":"dbplatform-api","event":"review.approved","database_id":"30000000-0000-4000-8000-000000000001","change_request_id":"01a1e910-ac30-74ab-a0e3-2e688f2140f6","migration_id":"01a1e910-ac1c-7132-8a16-92c50b525ed9","migration_revision":1,"status":"pending_review","request_id":"AAJDTAWAXE3FJSFFMQ56NYRHWB","actor_id":"10000000-0000-4000-8000-000000000002","trace_id":"9d8bce0b5d28a552143b156bb8d9943e","span_id":"d02d37fa070fac69"}
{"time":"2026-11-17T10:32:26.50949488Z","level":"INFO","msg":"audit","service":"dbplatform-api","event":"audit","action":"request.approved","actor_id":"10000000-0000-4000-8000-000000000002","actor_name":"viewer","database_id":"30000000-0000-4000-8000-000000000001","resource_type":"change_request","resource_id":"01a1e910-ac30-74ab-a0e3-2e688f2140f6","details":{"action":"apply","content_hash":"f54253075b80d153c479ea2c646d80be68070e7f39dc0d19ebbcacbe583db07f","group_ids":["20000000-0000-4000-8000-000000000001"],"migration_id":"01a1e910-ac1c-7132-8a16-92c50b525ed9","revision":1,"status":"pending_review"},"request_id":"AAJDTAWAXE3FJSFFMQ56NYRHWB","trace_id":"9d8bce0b5d28a552143b156bb8d9943e","span_id":"d02d37fa070fac69"}
{"time":"2026-11-17T10:32:26.563745713Z","level":"INFO","msg":"change request rejected","service":"dbplatform-api","event":"review.rejected","database_id":"30000000-0000-4000-8000-000000000001","change_request_id":"01a1e910-ac5b-7ae3-9e08-80bf0c8419b0","migration_id":"01a1e910-ac51-7e36-a4e1-799ba6595ed9","migration_revision":1,"status":"rejected","request_id":"4K3KOPGJQLK7IXJ7NU4KO2AMN7","actor_id":"10000000-0000-4000-8000-000000000005","trace_id":"86a6d407feb9b4c2bbeedb542800b319","span_id":"d299938223bd9ced"}
{"time":"2026-11-17T10:32:26.563858647Z","level":"INFO","msg":"audit","service":"dbplatform-api","event":"audit","action":"request.rejected","actor_id":"10000000-0000-4000-8000-000000000005","actor_name":"lead","database_id":"30000000-0000-4000-8000-000000000001","resource_type":"change_request","resource_id":"01a1e910-ac5b-7ae3-9e08-80bf0c8419b0","details":{"action":"apply","content_hash":"56d0af30e8be2fe8b8cc808d87f744192aa8b8a1a45141352988c619ee5513b0","group_ids":["20000000-0000-4000-8000-000000000002"],"migration_id":"01a1e910-ac51-7e36-a4e1-799ba6595ed9","revision":1,"status":"rejected"},"request_id":"4K3KOPGJQLK7IXJ7NU4KO2AMN7","trace_id":"86a6d407feb9b4c2bbeedb542800b319","span_id":"d299938223bd9ced"}
```

## Eksekusi sukses

Dari API ke worker dalam satu trace (`210df2750aaf4629e5de23dd3c577e66`). `outbox.published` ditulis relay di trace sendiri:

```json
{"time":"2026-11-17T10:32:26.587487105Z","level":"INFO","msg":"execution accepted","service":"dbplatform-api","event":"execution.enqueued","execution_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","job_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","database_id":"30000000-0000-4000-8000-000000000001","change_request_id":"01a1e910-ac30-74ab-a0e3-2e688f2140f6","migration_id":"01a1e910-ac1c-7132-8a16-92c50b525ed9","migration_revision":1,"status":"queued","request_id":"PUJ3EIPITYPUZ576R4WLVHIKAW","actor_id":"10000000-0000-4000-8000-000000000003","trace_id":"210df2750aaf4629e5de23dd3c577e66","span_id":"b65ab2f6195d0efa"}
{"time":"2026-11-17T10:32:26.590676075Z","level":"INFO","msg":"job published","service":"dbplatform-api","event":"outbox.published","execution_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","job_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","database_id":"30000000-0000-4000-8000-000000000001","trace_id":"315a8a4eb2c0a1d858be7469cec9ebfb","span_id":"81857784c61b3df2"}
{"time":"2026-11-17T10:32:26.591128812Z","level":"INFO","msg":"job received","service":"dbplatform-worker","event":"job.received","execution_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","job_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","database_id":"30000000-0000-4000-8000-000000000001","delivery":1,"trace_id":"210df2750aaf4629e5de23dd3c577e66","span_id":"8ec849beb3fbbaf3"}
{"time":"2026-11-17T10:32:26.623494754Z","level":"INFO","msg":"lock.acquired","service":"dbplatform-worker","event":"lock.acquired","execution_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","job_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1e910-ac1c-7132-8a16-92c50b525ed9","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"running","direction":"up","change_request_id":"01a1e910-ac30-74ab-a0e3-2e688f2140f6","trace_id":"210df2750aaf4629e5de23dd3c577e66","span_id":"8ec849beb3fbbaf3"}
{"time":"2026-11-17T10:32:26.623649036Z","level":"INFO","msg":"execution.started","service":"dbplatform-worker","event":"execution.started","execution_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","job_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1e910-ac1c-7132-8a16-92c50b525ed9","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"running","direction":"up","change_request_id":"01a1e910-ac30-74ab-a0e3-2e688f2140f6","trace_id":"210df2750aaf4629e5de23dd3c577e66","span_id":"8ec849beb3fbbaf3"}
{"time":"2026-11-17T10:32:26.660520825Z","level":"INFO","msg":"execution.revalidated","service":"dbplatform-worker","event":"execution.revalidated","execution_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","job_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1e910-ac1c-7132-8a16-92c50b525ed9","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"running","direction":"up","change_request_id":"01a1e910-ac30-74ab-a0e3-2e688f2140f6","trace_id":"210df2750aaf4629e5de23dd3c577e66","span_id":"0a04ce4b6f03ca71"}
{"time":"2026-11-17T10:32:26.631621601Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.tx.begin","db.pool":"target","db.operation":"BEGIN","db.query":"BEGIN","db.params":[],"duration_ms":0.103,"database_id":"30000000-0000-4000-8000-000000000001","rows":0,"status":"ok","execution_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","job_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","actor_id":"10000000-0000-4000-8000-000000000003","migration_id":"01a1e910-ac1c-7132-8a16-92c50b525ed9","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e910-ac30-74ab-a0e3-2e688f2140f6","trace_id":"210df2750aaf4629e5de23dd3c577e66","span_id":"18e92757a9c1dcac"}
{"time":"2026-11-17T10:32:26.664310294Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.query","db.pool":"target","db.operation":"INSERT","db.query":"INSERT INTO log_demo_1794911546 VALUES ($1, $2)","db.params":[],"duration_ms":0.34,"database_id":"30000000-0000-4000-8000-000000000001","rows":1,"status":"ok","execution_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","job_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","actor_id":"10000000-0000-4000-8000-000000000003","migration_id":"01a1e910-ac1c-7132-8a16-92c50b525ed9","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e910-ac30-74ab-a0e3-2e688f2140f6","trace_id":"210df2750aaf4629e5de23dd3c577e66","span_id":"22e28c600dc0f793"}
{"time":"2026-11-17T10:32:26.667614927Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.tx.commit","db.pool":"target","db.operation":"COMMIT","db.query":"COMMIT","db.params":[],"duration_ms":0.879,"database_id":"30000000-0000-4000-8000-000000000001","rows":0,"status":"ok","execution_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","job_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","actor_id":"10000000-0000-4000-8000-000000000003","migration_id":"01a1e910-ac1c-7132-8a16-92c50b525ed9","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e910-ac30-74ab-a0e3-2e688f2140f6","trace_id":"210df2750aaf4629e5de23dd3c577e66","span_id":"ddd6534925c4134e"}
{"time":"2026-11-17T10:32:26.677091267Z","level":"INFO","msg":"audit","service":"dbplatform-worker","event":"audit","action":"history.recorded","actor_id":"10000000-0000-4000-8000-000000000003","actor_name":"editor","database_id":"30000000-0000-4000-8000-000000000001","resource_type":"execution","resource_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","details":{"seq":1,"version":1},"trace_id":"210df2750aaf4629e5de23dd3c577e66","span_id":"4f0c75ff3201ca5c"}
{"time":"2026-11-17T10:32:26.676896132Z","level":"INFO","msg":"execution.finished","service":"dbplatform-worker","event":"execution.finished","execution_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","job_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1e910-ac1c-7132-8a16-92c50b525ed9","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"succeeded","direction":"up","change_request_id":"01a1e910-ac30-74ab-a0e3-2e688f2140f6","trace_id":"210df2750aaf4629e5de23dd3c577e66","span_id":"4f0c75ff3201ca5c"}
{"time":"2026-11-17T10:32:26.677204854Z","level":"INFO","msg":"lock.released","service":"dbplatform-worker","event":"lock.released","execution_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","job_id":"01a1e910-acd2-7d2e-942e-4f26669889ae","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1e910-ac1c-7132-8a16-92c50b525ed9","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"running","direction":"up","change_request_id":"01a1e910-ac30-74ab-a0e3-2e688f2140f6","trace_id":"210df2750aaf4629e5de23dd3c577e66","span_id":"8ec849beb3fbbaf3"}
```

## Eksekusi gagal

Statement kedua melanggar primary key (`sqlstate` `23505`). Seluruh transaksi target di-rollback (`db.tx.rollback`, rollback transaksi, bukan rollback migration), execution berakhir `failed` dengan `error_code`, dan tidak ada `history.recorded`:

```json
{"time":"2026-11-17T10:32:27.169739276Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.query","db.pool":"target","db.operation":"INSERT","db.query":"INSERT INTO log_demo_1794911546 VALUES ($1, $2), ($3, $4)","db.params":[],"duration_ms":0.911,"database_id":"30000000-0000-4000-8000-000000000001","sqlstate":"23505","status":"error","execution_id":"01a1e910-af07-7b5a-b655-bb2431218cb8","job_id":"01a1e910-af07-7b5a-b655-bb2431218cb8","actor_id":"10000000-0000-4000-8000-000000000003","migration_id":"01a1e910-ac3c-734e-80d4-f20dfa7f7b13","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e910-ac47-7d4e-a11f-ac374d0f9a9b","trace_id":"48281060fb9a862a38a0e1c373335889","span_id":"cb4f52f5126d0ec8"}
{"time":"2026-11-17T10:32:27.17008651Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.tx.rollback","db.pool":"target","db.operation":"ROLLBACK","db.query":"ROLLBACK","db.params":[],"duration_ms":0.218,"database_id":"30000000-0000-4000-8000-000000000001","rows":0,"status":"ok","execution_id":"01a1e910-af07-7b5a-b655-bb2431218cb8","job_id":"01a1e910-af07-7b5a-b655-bb2431218cb8","actor_id":"10000000-0000-4000-8000-000000000003","migration_id":"01a1e910-ac3c-734e-80d4-f20dfa7f7b13","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e910-ac47-7d4e-a11f-ac374d0f9a9b","trace_id":"48281060fb9a862a38a0e1c373335889","span_id":"d305ef00bfc27bc3"}
{"time":"2026-11-17T10:32:27.17723592Z","level":"INFO","msg":"execution.finished","service":"dbplatform-worker","event":"execution.finished","execution_id":"01a1e910-af07-7b5a-b655-bb2431218cb8","job_id":"01a1e910-af07-7b5a-b655-bb2431218cb8","database_id":"30000000-0000-4000-8000-000000000001","migration_id":"01a1e910-ac3c-734e-80d4-f20dfa7f7b13","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000003","status":"failed","direction":"up","change_request_id":"01a1e910-ac47-7d4e-a11f-ac374d0f9a9b","error_code":"SQL_EXECUTION_FAILED","trace_id":"48281060fb9a862a38a0e1c373335889","span_id":"efd18739b90db83f"}
```

## Audit admin

Reset password hanya mencatat jenis perubahannya; password baru tidak pernah ditulis:

```json
{"time":"2026-11-17T10:32:26.205748115Z","level":"INFO","msg":"audit","service":"dbplatform-api","event":"audit","action":"user.changed","actor_id":"10000000-0000-4000-8000-000000000001","actor_name":"fixture-admin","resource_type":"user","resource_id":"10000000-0000-4000-8000-000000000004","details":{"change":"password_reset"},"request_id":"HLIYN5DIEBYASU6BE7QBTJMSIR","trace_id":"8c3df5afca9d452cb93d25657b728887","span_id":"d88eb79708c3fa84"}
```

## Belum tercakup

Event batch (`batch.committed`, `batch.failed`), `execution.retry_scheduled`, `execution.held`, `lock.busy`, dan `recovery.evidence_checked` belum muncul di sesi ini. Contohnya ditambahkan setelah mode batch dan alur retry tersedia.
