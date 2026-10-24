Atlas Community is used only to generate PostgreSQL SQL candidates. The manual migration runner does not depend on Atlas. SQLite uses the native generator.

`schemadiff.AtlasGenerator` implements the generator interface. `Check(ctx)` verifies the installed version; `Generate(ctx, comparison)` checks it again before reading the registered databases. An empty `Binary` uses `atlas` from PATH. The server probes Atlas at startup with a maximum of two seconds and enables PostgreSQL's `schema_diff` capability on success. An unavailable Atlas does not prevent startup or manual migrations. The secured endpoint is `POST /api/v1/databases/{id}/schema-diff`; its request/response and fixture setup are described in the root README.

`WithDiffConfig` supplies a temporary `atlas.hcl` with two environments. `source.url` points to the database being changed, `source.schema.src` points to the comparison database, and `desired.url` points to that same comparison database. The file contains credentials, has mode 0600, and exists only for the callback.

Run the generator inside that callback with arguments:

```text
atlas schema diff --config file://<configPath> --env source --from env://url --to env://schema.src --schema public --format '{{ sql . }}'
```

`env://` selects an attribute of the environment chosen with `--env`. It does not select another environment by name.

The explicit format returns SQL only, including an empty string for identical schemas. Schema names are encoded as one CSV field for Atlas's schema flag, so a comma in an identifier cannot select an additional schema.

The process has a 30-second deadline by default, configured by `DIFF_TIMEOUT`. Stdout and stderr are each limited to 1 MiB; exceeding either limit cancels the process. Candidate SQL must fit the draft limit of 256 KiB and contain valid UTF-8 without NUL. Stderr is never returned to the caller. Missing binaries, a different version, process failures, and invalid output return `DIFF_UNAVAILABLE`.

The PostgreSQL parser accepts only supported transactional table, column, constraint, and index DDL in the selected schema. Transaction control, concurrent index operations, role changes, reserved objects, and other unsupported output produce an empty, non-executable candidate with a warning. The service rechecks both fingerprints and the source migration head after generation. Applying a reviewed draft still goes through the migration runner's own guard, role, lock, and fingerprint checks.

The child receives a private HOME and an environment allowlist, including `ATLAS_NO_UPDATE_NOTIFIER=1` and `ATLAS_NO_UPGRADE_SUGGESTIONS=1`. It does not inherit application credentials, Cloud tokens, or PostgreSQL environment variables. The Community entry point has no telemetry initialization. HOME and the registry's temporary config are removed after success or failure. These behaviors are checked against the [v1.3.0 entry point](https://github.com/ariga/atlas/blob/v1.3.0/cmd/atlas/main.go) and [upgrade suggestion implementation](https://github.com/ariga/atlas/blob/v1.3.0/cmd/atlas/internal/cmdapi/cmdapi.go).

The Docker build compiles the Community source at commit `9a6bc601212130aaaefcbc8dd36c710baf9716ff` (`v1.3.0`). The archive SHA-256 is `15f14b25db8e32f6fc91343c77a3e624507a1b051f91fecbcfc643cb596c0321`. It installs the binary at `/usr/local/bin/atlas`, sets `ATLAS_BIN` to that path, and includes the license. To verify the runtime binary without database or internet access, from the repository root:

```sh
docker build -f infra/docker/Dockerfile -t if3110-atlas-check .
docker run --rm --network none --entrypoint /usr/local/bin/atlas \
  -e ATLAS_NO_UPDATE_NOTIFIER=1 -e ATLAS_NO_UPGRADE_SUGGESTIONS=1 \
  if3110-atlas-check version
```

Process and parser tests run with the ordinary backend suite and need no Atlas installation:

```sh
sh scripts/test-backend.sh -race -run '^TestAtlas' ./internal/schemadiff
```

The separate real-Atlas suite requires `ATLAS_BIN`, `TEST_METADATA_DATABASE_URL`, and `TEST_TARGET_DATABASE_URL`. Use isolated PostgreSQL test databases. The target server must also have `demo_desired`, accessible with the same runner credentials and editor role. Load `demo.sql` in both databases and `desired.sql` in the comparison database. The editor additionally needs CREATE on these two test databases so the integration test can create and remove its own schemas. CI prepares these permissions in the Bonus Atlas job; they are fixture permissions, not a new application requirement.

```sh
cd backend
go test -tags atlas -p 1 -count=1 -timeout=180s \
  -run '^TestAtlas(PostgresCandidateIntegration|ConfigSmoke)$|^TestPostgresSchemaDiffHTTP$' \
  ./internal/connections ./internal/schemadiff ./cmd/server
```

Run the packages serially: the suites use full fingerprints on the same test target server. The config smoke checks generation without applying SQL. The generator integration checks identical and changed schemas, data/schema preservation during generation, and config cleanup, then explicitly saves and executes the candidate through the real migration runner. It covers create/drop table, alter column, index, PK, FK, unique, and check constraints. The HTTP suite checks the real server composition, source readiness, desired view access, capability, and behavior with a missing Atlas binary.

To prove operation without external network access, run this suite in a network that can reach only its test PostgreSQL server and set `ATLAS_OFFLINE_TEST=1`. The test first checks that external TCP connections are blocked, then runs the real generator. Merely setting this variable does not isolate the network. CI runs the normal separate Atlas suite; network isolation must be supplied by the test environment.
