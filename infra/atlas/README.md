Atlas Community is used only to generate SQL. The manual migration runner does not depend on Atlas.

`WithDiffConfig` supplies a temporary `atlas.hcl` with two environments. `source.url` points to the database being changed, `source.schema.src` points to the comparison database, and `desired.url` points to that same comparison database. The file contains credentials, has mode 0600, and exists only for the callback.

Run the generator inside that callback with arguments:

```text
atlas schema diff --config file://<configPath> --env source --from env://url --to env://schema.src --schema public
```

`env://` selects an attribute of the environment chosen with `--env`. It does not select another environment by name.

Use the Community source tag `v1.3.0`. The separate smoke test requires `ATLAS_BIN`, `TEST_METADATA_DATABASE_URL`, and `TEST_TARGET_DATABASE_URL`. The target test server must also have a database named `demo_desired`, accessible with the target runner credentials and the same editor role configuration.

```sh
cd backend
go test -tags atlas -count=1 -run '^TestAtlasConfigSmoke$' ./internal/connections
```

The test compares real PostgreSQL databases, checks that candidate SQL contains the desired change, verifies that the source fingerprint stays unchanged, and checks temporary-file cleanup. It never applies generated SQL.

The child process gets a private HOME, `ATLAS_NO_UPDATE_NOTIFIER=1`, and `ATLAS_NO_UPGRADE_SUGGESTIONS=1`. Community v1.3.0 has no telemetry initialization in its OSS entry point. No login or Cloud token is supplied. Stdout and stderr are each limited to 1 MiB; the test has a 30-second deadline.
