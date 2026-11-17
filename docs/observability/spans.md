# Contoh span (console exporter)

Output nyata dari satu eksekusi `apply` lewat API pada 17 November 2026: `lead` membuat migration di `load_1` (`UPDATE public.load_items SET qty = qty + 1 WHERE id BETWEEN 1 AND 10`), mengajukan request, meng-approve, lalu menekan execute. API dan worker berjalan dengan `OTEL_TRACES_EXPORTER=console` (default Compose); setiap span dicetak exporter sebagai satu baris JSON di stdout container, di antara log JSON.

```sh
sh scripts/app-up.sh seed
docker compose --env-file .env -f infra/docker/compose.yml --profile app logs --no-log-prefix backend worker | grep '"Name":'
```

## Pohon span

Satu trace (`3421a885a5b0a8491c730fe7ff5fe725`, 77 span) dari request HTTP sampai SQL di worker. `migration.job` menjadi anak `outbox.enqueue` karena `traceparent` disimpan di outbox saat enqueue dan dibawa pesan NATS. `outbox.publish` (relay) berada di trace sendiri dan merujuk `outbox.enqueue` lewat link. Query yang panjang dipotong.

```text
POST /api/v1/databases/{id}/change-requests/{rid}/execute  [api, server]
  db.query metadata begin isolation level read committed  [api, client]
  db.query metadata SELECT session_id FROM session_tokens WHERE token_hash=$1  [api, client]
  db.query metadata SELECT user_id, absolute_expires_at, last_seen_at, rotated_at, generat  [api, client]
  db.query metadata SELECT is_current, generation, valid_until FROM session_tokens WHERE t  [api, client]
  db.query metadata SELECT id, username, is_platform_admin FROM users WHERE id=$1 AND acti  [api, client]
  db.query metadata UPDATE sessions SET last_seen_at=$2 WHERE id=$1  [api, client]
  db.query metadata commit  [api, client]
  db.query metadata SELECT active, is_platform_admin FROM users WHERE id=$1  [api, client]
  db.query metadata SELECT CASE WHEN bool_or(g.level='edit') THEN 'edit' WHEN bool_or(g.le  [api, client]
  db.query metadata begin  [api, client]
  db.query metadata SELECT id FROM database_connections WHERE id=$1 FOR UPDATE  [api, client]
  db.query metadata SELECT migration_id FROM change_requests WHERE database_id=$1 AND id=$  [api, client]
  db.query metadata SELECT id FROM migrations WHERE database_id=$1 AND id=$2 FOR UPDATE  [api, client]
  db.query metadata SELECT id, database_id, migration_id, action, migration_revision, cont  [api, client]
  db.query metadata SELECT g.id, g.name, u.id, u.username, a.decision, a.comment, a.create  [api, client]
  db.query metadata SELECT m.id, m.database_id, m.title, m.description, m.status, m.versio  [api, client]
  db.query metadata SELECT coalesce((SELECT head_after FROM migration_history WHERE databa  [api, client]
  db.query metadata SELECT id, reason FROM ( SELECT id, 'outcome_unknown' AS reason, 0 AS   [api, client]
  db.query metadata INSERT INTO migration_executions (id,database_id,number,migration_id,c  [api, client]
  db.query metadata UPDATE change_requests SET status='executed', updated_at=$2, closed_at  [api, client]
  outbox.enqueue  [api, internal]
    db.query metadata INSERT INTO outbox_messages (id, subject, payload, headers) VALUES ($1  [api, client]
    migration.job reason=done  [worker, consumer]
      db.query metadata begin  [worker, client]
      db.query metadata SELECT id FROM database_connections WHERE id=$1 FOR UPDATE  [worker, client]
      db.query metadata SELECT e.id, e.number, e.database_id, e.migration_id, m.title, e.chang  [worker, client]
      db.query metadata SELECT id, reason FROM ( SELECT id, 'outcome_unknown' AS reason, 0 AS   [worker, client]
      db.query metadata SELECT EXISTS (SELECT 1 FROM migration_executions WHERE database_id=$1  [worker, client]
      db.query metadata UPDATE migration_executions SET lock_requested_at=$2 WHERE id=$1  [worker, client]
      db.query metadata commit  [worker, client]
      db.query metadata SELECT id, name, driver, config_encrypted, revision, active, migration  [worker, client]
      db.query target SELECT count(DISTINCT relname) = 2 FROM pg_class WHERE relname IN ('ap  [worker, client]
      lock.acquire outcome=acquired wait_ms=0  [worker, internal]
        db.query target SELECT pg_try_advisory_lock(hashtextextended('if3110.migration', 0))  [worker, client]
      db.query metadata begin  [worker, client]
      db.query metadata SELECT id FROM database_connections WHERE id=$1 FOR UPDATE  [worker, client]
      db.query metadata SELECT e.id, e.number, e.database_id, e.migration_id, m.title, e.chang  [worker, client]
      db.query metadata SELECT id, reason FROM ( SELECT id, 'outcome_unknown' AS reason, 0 AS   [worker, client]
      db.query metadata SELECT EXISTS (SELECT 1 FROM migration_executions WHERE database_id=$1  [worker, client]
      db.query metadata UPDATE migration_executions SET status='running', attempt=$2, worker_i  [worker, client]
      db.query metadata commit  [worker, client]
      execution.revalidate  [worker, internal]
        db.query metadata SELECT CASE WHEN bool_or(g.level='edit') THEN 'edit' WHEN bool_or(g.le  [worker, client]
        db.query metadata SELECT id, database_id, migration_id, action, migration_revision, cont  [worker, client]
        db.query metadata SELECT g.id, g.name, u.id, u.username, a.decision, a.comment, a.create  [worker, client]
        db.query metadata SELECT m.id, m.database_id, m.title, m.description, m.status, m.versio  [worker, client]
        db.query target BEGIN  [worker, client]
        db.query target SELECT set_config('statement_timeout',$1,true), set_config('lock_timeo  [worker, client]
        db.query target SELECT seq,head_after FROM _platform.migration_history ORDER BY seq DE  [worker, client]
        db.query target SET LOCAL ROLE "platform_editor"  [worker, client]
        db.query metadata begin  [worker, client]
        db.query metadata SELECT id FROM migrations WHERE database_id=$1 AND id=$2 FOR UPDATE  [worker, client]
        db.query metadata SELECT CASE WHEN bool_or(g.level='edit') THEN 'edit' WHEN bool_or(g.le  [worker, client]
        db.query metadata SELECT id, database_id, migration_id, action, migration_revision, cont  [worker, client]
        db.query metadata SELECT g.id, g.name, u.id, u.username, a.decision, a.comment, a.create  [worker, client]
        db.query metadata SELECT m.id, m.database_id, m.title, m.description, m.status, m.versio  [worker, client]
        db.query metadata UPDATE migrations SET locked_at=$2, locked_revision=$3 WHERE id=$1 AND  [worker, client]
        db.query metadata commit  [worker, client]
      db.query target UPDATE public.load_items SET qty = qty + $1 WHERE id BETWEEN $2 AND $3  [worker, client]
      db.query target RESET ROLE  [worker, client]
      db.query target INSERT INTO _platform.migration_history (seq, execution_id, migration_  [worker, client]
      db.query target COMMIT  [worker, client]
      execution.finalize  [worker, internal]
        db.query metadata begin  [worker, client]
        db.query metadata SELECT id FROM database_connections WHERE id=$1 FOR UPDATE  [worker, client]
        db.query metadata SELECT e.id, e.number, e.database_id, e.migration_id, m.title, e.chang  [worker, client]
        db.query metadata INSERT INTO migration_history (database_id,seq, execution_id, migratio  [worker, client]
        db.query metadata UPDATE migrations SET status='superseded', updated_at=$3 WHERE databas  [worker, client]
        db.query metadata UPDATE migrations SET status=$2, version=$3, updated_at=$4, updated_by  [worker, client]
        db.query metadata UPDATE migration_executions SET status=$2, attempt=$3, next_attempt_at  [worker, client]
        db.query metadata SELECT id, reason FROM ( SELECT id, 'outcome_unknown' AS reason, 0 AS   [worker, client]
        db.query metadata UPDATE migration_executions SET status='queued', held_reason=NULL WHER  [worker, client]
        db.query metadata SELECT e.id, e.number, e.database_id, e.migration_id, m.title, e.chang  [worker, client]
        db.query metadata commit  [worker, client]
  db.query metadata SELECT e.id, e.number, e.database_id, e.migration_id, m.title, e.chang  [api, client]
  db.query metadata commit  [api, client]
outbox.publish  [api, producer, link]```

`lock.acquire` membungkus `pg_try_advisory_lock` pada koneksi yang dipin; SQL migration, `INSERT` history target, dan `COMMIT` berjalan di koneksi yang sama. Metadata pool dicatat sebagai span (log-nya level DEBUG), target pool sebagai span dan log INFO.

## Contoh span lengkap

Atribut resource sama pada semua span:

```json
"Resource": [{"Key":"service.name","Value":{"Type":"STRING","Value":"dbplatform-worker"}},{"Key":"service.version","Value":{"Type":"STRING","Value":"dev"}},{"Key":"telemetry.sdk.language","Value":{"Type":"STRING","Value":"go"}},{"Key":"telemetry.sdk.name","Value":{"Type":"STRING","Value":"opentelemetry"}},{"Key":"telemetry.sdk.version","Value":{"Type":"STRING","Value":"1.47.0"}}]
```

### `lock.acquire` (worker)

```json
{
  "Name": "lock.acquire",
  "SpanContext": {
    "TraceID": "3421a885a5b0a8491c730fe7ff5fe725",
    "SpanID": "f871fde5ff21efa5",
    "TraceFlags": "01",
    "TraceState": "",
    "Remote": false
  },
  "Parent": {
    "TraceID": "3421a885a5b0a8491c730fe7ff5fe725",
    "SpanID": "b010430a48d9e923",
    "TraceFlags": "01",
    "TraceState": "",
    "Remote": false
  },
  "SpanKind": 1,
  "StartTime": "2026-11-17T03:35:48.560006103Z",
  "EndTime": "2026-11-17T03:35:48.561209419Z",
  "Attributes": [
    {
      "Key": "outcome",
      "Value": {
        "Type": "STRING",
        "Value": "acquired"
      }
    },
    {
      "Key": "wait_ms",
      "Value": {
        "Type": "INT64",
        "Value": 0
      }
    }
  ],
  "Events": null,
  "Links": null,
  "Status": {
    "Code": "Unset",
    "Description": ""
  },
  "DroppedAttributes": 0,
  "DroppedEvents": 0,
  "DroppedLinks": 0,
  "ChildSpanCount": 1,
  "InstrumentationScope": {
    "Name": "github.com/NeswaEA/IF3110_K01_G03/backend/internal/telemetry",
    "Version": "",
    "SchemaURL": "",
    "Attributes": null
  }
}
```

### `db.query` untuk SQL pengguna (worker, target)

SQL pengguna dinormalisasi `pg_query.Normalize` sebelum dicatat: literal `1`, `1`, `10` menjadi `$1`, `$2`, `$3`, sehingga nilai data tidak muncul di span maupun log. `db.params` hanya berisi tipe parameter yang benar-benar dikirim (di sini tidak ada).

```json
{
  "Name": "db.query",
  "SpanContext": {
    "TraceID": "3421a885a5b0a8491c730fe7ff5fe725",
    "SpanID": "1547adf03439d992",
    "TraceFlags": "01",
    "TraceState": "",
    "Remote": false
  },
  "Parent": {
    "TraceID": "3421a885a5b0a8491c730fe7ff5fe725",
    "SpanID": "b010430a48d9e923",
    "TraceFlags": "01",
    "TraceState": "",
    "Remote": false
  },
  "SpanKind": 3,
  "StartTime": "2026-11-17T03:35:48.61049583Z",
  "EndTime": "2026-11-17T03:35:48.612050271Z",
  "Attributes": [
    {
      "Key": "db.pool",
      "Value": {
        "Type": "STRING",
        "Value": "target"
      }
    },
    {
      "Key": "db.operation",
      "Value": {
        "Type": "STRING",
        "Value": "UPDATE"
      }
    },
    {
      "Key": "db.query",
      "Value": {
        "Type": "STRING",
        "Value": "UPDATE public.load_items SET qty = qty + $1 WHERE id BETWEEN $2 AND $3"
      }
    },
    {
      "Key": "db.params",
      "Value": {
        "Type": "STRINGSLICE",
        "Value": []
      }
    },
    {
      "Key": "database_id",
      "Value": {
        "Type": "STRING",
        "Value": "30000000-0000-4000-8000-000000000011"
      }
    },
    {
      "Key": "rows",
      "Value": {
        "Type": "INT64",
        "Value": 10
      }
    }
  ],
  "Events": null,
  "Links": null,
  "Status": {
    "Code": "Unset",
    "Description": ""
  },
  "DroppedAttributes": 0,
  "DroppedEvents": 0,
  "DroppedLinks": 0,
  "ChildSpanCount": 0,
  "InstrumentationScope": {
    "Name": "github.com/NeswaEA/IF3110_K01_G03/backend/internal/telemetry",
    "Version": "",
    "SchemaURL": "",
    "Attributes": null
  }
}
```

### `migration.job` dan `outbox.publish`

Ringkasan dua span lintas proses (atribut diratakan): parent `migration.job` adalah span `outbox.enqueue` milik API (`Remote: true`), sedangkan `outbox.publish` memulai trace baru dengan link ke span yang sama.

```json
{"Name":"migration.job","SpanContext":{"TraceID":"3421a885a5b0a8491c730fe7ff5fe725","SpanID":"b010430a48d9e923"},"Parent":{"TraceID":"3421a885a5b0a8491c730fe7ff5fe725","SpanID":"c596ea27e76a6552","Remote":true},"SpanKind":5,"StartTime":"2026-11-17T03:35:48.499451918Z","EndTime":"2026-11-17T03:35:48.6330843Z","Attributes":{"execution_id":"01a1e793-3bff-7026-8386-4b986feedff5","database_id":"30000000-0000-4000-8000-000000000011","reason":"done"},"Links":[],"Service":"dbplatform-worker"}
{"Name":"outbox.publish","SpanContext":{"TraceID":"ec19ee8292eef8d7e7d01a571924a209","SpanID":"43afccd1dfe7aa53"},"Parent":{"TraceID":"00000000000000000000000000000000","SpanID":"0000000000000000","Remote":false},"SpanKind":4,"StartTime":"2026-11-17T03:35:48.492084604Z","EndTime":"2026-11-17T03:35:48.49953997Z","Attributes":{"execution_id":"01a1e793-3bff-7026-8386-4b986feedff5","database_id":"30000000-0000-4000-8000-000000000011"},"Links":[{"TraceID":"3421a885a5b0a8491c730fe7ff5fe725","SpanID":"c596ea27e76a6552"}],"Service":"dbplatform-api"}
```

## Log yang berkorelasi

Log JSON yang ditulis selama span aktif membawa `trace_id` dan `span_id` span tersebut. Baris `db.query` di bawah memiliki `span_id` `1547adf03439d992`, sama dengan span `db.query` SQL pengguna di atas; event job memakai `span_id` `migration.job` (`b010430a48d9e923`).

```json
{"time":"2026-11-17T03:35:48.49025789Z","level":"INFO","msg":"execution accepted","service":"dbplatform-api","event":"execution.enqueued","execution_id":"01a1e793-3bff-7026-8386-4b986feedff5","job_id":"01a1e793-3bff-7026-8386-4b986feedff5","database_id":"30000000-0000-4000-8000-000000000011","change_request_id":"01a1e793-3960-7990-a89a-6555f00f8bb6","migration_id":"01a1e793-37ff-7686-b86b-87a524f26c81","migration_revision":1,"status":"queued","request_id":"NWVOCBGSHC2HAQA4EQW6VJFFT2","actor_id":"10000000-0000-4000-8000-000000000005","trace_id":"3421a885a5b0a8491c730fe7ff5fe725","span_id":"62ac21bb3160c2d2"}
{"time":"2026-11-17T03:35:48.50060878Z","level":"INFO","msg":"job received","service":"dbplatform-worker","event":"job.received","execution_id":"01a1e793-3bff-7026-8386-4b986feedff5","job_id":"01a1e793-3bff-7026-8386-4b986feedff5","database_id":"30000000-0000-4000-8000-000000000011","delivery":1,"trace_id":"3421a885a5b0a8491c730fe7ff5fe725","span_id":"b010430a48d9e923"}
{"time":"2026-11-17T03:35:48.569737275Z","level":"INFO","msg":"lock.acquired","service":"dbplatform-worker","event":"lock.acquired","execution_id":"01a1e793-3bff-7026-8386-4b986feedff5","job_id":"01a1e793-3bff-7026-8386-4b986feedff5","database_id":"30000000-0000-4000-8000-000000000011","migration_id":"01a1e793-37ff-7686-b86b-87a524f26c81","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000005","status":"running","direction":"up","change_request_id":"01a1e793-3960-7990-a89a-6555f00f8bb6","trace_id":"3421a885a5b0a8491c730fe7ff5fe725","span_id":"b010430a48d9e923"}
{"time":"2026-11-17T03:35:48.580983827Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.tx.begin","db.pool":"target","db.operation":"BEGIN","db.query":"BEGIN","db.params":[],"duration_ms":0.228,"database_id":"30000000-0000-4000-8000-000000000011","rows":0,"status":"ok","execution_id":"01a1e793-3bff-7026-8386-4b986feedff5","job_id":"01a1e793-3bff-7026-8386-4b986feedff5","actor_id":"10000000-0000-4000-8000-000000000005","migration_id":"01a1e793-37ff-7686-b86b-87a524f26c81","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e793-3960-7990-a89a-6555f00f8bb6","trace_id":"3421a885a5b0a8491c730fe7ff5fe725","span_id":"84f6c12536879b30"}
{"time":"2026-11-17T03:35:48.631103151Z","level":"INFO","msg":"execution.finished","service":"dbplatform-worker","event":"execution.finished","execution_id":"01a1e793-3bff-7026-8386-4b986feedff5","job_id":"01a1e793-3bff-7026-8386-4b986feedff5","database_id":"30000000-0000-4000-8000-000000000011","migration_id":"01a1e793-37ff-7686-b86b-87a524f26c81","migration_revision":1,"attempt":1,"actor_id":"10000000-0000-4000-8000-000000000005","status":"succeeded","direction":"up","change_request_id":"01a1e793-3960-7990-a89a-6555f00f8bb6","trace_id":"3421a885a5b0a8491c730fe7ff5fe725","span_id":"8358756ff67af8ce"}
{"time":"2026-11-17T03:35:48.611951774Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.query","db.pool":"target","db.operation":"UPDATE","db.query":"UPDATE public.load_items SET qty = qty + $1 WHERE id BETWEEN $2 AND $3","db.params":[],"duration_ms":1.443,"database_id":"30000000-0000-4000-8000-000000000011","rows":10,"status":"ok","execution_id":"01a1e793-3bff-7026-8386-4b986feedff5","job_id":"01a1e793-3bff-7026-8386-4b986feedff5","actor_id":"10000000-0000-4000-8000-000000000005","migration_id":"01a1e793-37ff-7686-b86b-87a524f26c81","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e793-3960-7990-a89a-6555f00f8bb6","trace_id":"3421a885a5b0a8491c730fe7ff5fe725","span_id":"1547adf03439d992"}
{"time":"2026-11-17T03:35:48.616299691Z","level":"INFO","msg":"sql executed","service":"dbplatform-worker","event":"db.tx.commit","db.pool":"target","db.operation":"COMMIT","db.query":"COMMIT","db.params":[],"duration_ms":1.765,"database_id":"30000000-0000-4000-8000-000000000011","rows":0,"status":"ok","execution_id":"01a1e793-3bff-7026-8386-4b986feedff5","job_id":"01a1e793-3bff-7026-8386-4b986feedff5","actor_id":"10000000-0000-4000-8000-000000000005","migration_id":"01a1e793-37ff-7686-b86b-87a524f26c81","migration_revision":1,"attempt":1,"direction":"up","change_request_id":"01a1e793-3960-7990-a89a-6555f00f8bb6","trace_id":"3421a885a5b0a8491c730fe7ff5fe725","span_id":"4113997ffa08e2b8"}
```
