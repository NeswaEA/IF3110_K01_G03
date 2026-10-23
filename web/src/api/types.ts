// Shared API DTOs. Field names are the JSON names;
// do not rename them locally. Timestamps are RFC 3339 UTC strings.

export type Driver = "postgres" | "sqlite";
export type AccessLevel = "none" | "view" | "edit";
export type RunStatus = "running" | "succeeded" | "failed" | "outcome_unknown";
export type ExplainMode = "estimate";
export type ConstraintKind = "primary_key" | "foreign_key" | "unique" | "check";
export type Encoding = "text" | "decimal_string" | "boolean" | "number" | "timestamp" | "json" | "base64";
export type DiffChangeKind =
  | "add_table" | "drop_table" | "alter_table"
  | "add_column" | "drop_column" | "alter_column"
  | "add_index" | "drop_index" | "alter_index"
  | "add_constraint" | "drop_constraint" | "alter_constraint"
  | "other";

// Backend error codes plus client-only NETWORK_ERROR.
export type ErrorCode =
  | "INVALID_REQUEST" | "UNAUTHENTICATED" | "FORBIDDEN" | "CSRF_REJECTED" | "NOT_FOUND"
  | "REVISION_CONFLICT" | "VERSION_CONFLICT" | "RUN_IN_PROGRESS" | "IDEMPOTENCY_CONFLICT"
  | "TARGET_OUTCOME_UNKNOWN" | "UNSUPPORTED_SQL" | "SQL_EXECUTION_FAILED" | "PLAN_UNSUPPORTED"
  | "RATE_LIMITED" | "TARGET_UNAVAILABLE" | "TARGET_TIMEOUT" | "INTERNAL_ERROR"
  | "SCHEMA_CONFLICT" | "DRAFT_APPLIED" | "DUPLICATE_TARGET" | "LEDGER_INCOMPATIBLE"
  | "DIFF_UNSUPPORTED" | "DIFF_UNAVAILABLE" | "TARGET_NOT_READY" | "ALREADY_EXISTS" | "LAST_ADMIN"
  | "PAYLOAD_TOO_LARGE" | "EXECUTION_INTERRUPTED" | "NETWORK_ERROR";

export type Page = { limit: number; offset: number; has_more: boolean };
export type ApiResponse<T> = { data: T; page?: Page };

export type ApiError = {
  code: ErrorCode | string;
  message: string;
  fields: Record<string, string>;
  request_id: string;
  run_id?: string;
};

export type JSONValue = string | number | boolean | null | JSONValue[] | { [key: string]: JSONValue };

export type User = {
  id: string; username: string; is_platform_admin: boolean; active: boolean;
  created_at: string; updated_at: string;
};
export type CurrentUser = { id: string; username: string; is_platform_admin: boolean };
export type Group = { id: string; name: string; created_at: string; updated_at: string };
export type Grant = { database_id: string; level: "view" | "edit" };

export type Capabilities = {
  transactional_migrations: boolean; schema_diff: boolean; explain: boolean; explain_analyze: boolean;
};
export type DatabaseSummary = {
  id: string; name: string; driver: Driver; revision: number;
  effective_access: AccessLevel; capabilities: Capabilities;
};

export type PostgresPublicConfig = {
  host: string; port: number; database: string; username: string;
  sslmode: "disable" | "require" | "verify-ca" | "verify-full";
  editor_role: string; has_credentials: boolean;
};
export type SqlitePublicConfig = { file: string };
export type PublicConfig = PostgresPublicConfig | SqlitePublicConfig;
export type AdminConnection = {
  id: string; name: string; driver: Driver; revision: number; active: boolean;
  migration_ready: boolean; config: PublicConfig;
  last_checked_at: string | null; last_error_code: string | null;
};

export type Column = { name: string; data_type: string; nullable: boolean; default: string | null; ordinal: number };
export type ForeignReference = { schema: string; table: string; columns: string[]; on_update: string; on_delete: string };
export type Constraint = {
  name: string | null; kind: ConstraintKind; columns: string[]; definition: string;
  references: ForeignReference | null;
};
export type Index = { name: string; unique: boolean; primary: boolean; columns: string[]; definition: string };
export type Table = { name: string; columns: Column[]; constraints: Constraint[]; indexes: Index[] };
export type Schema = { name: string; tables: Table[] };
export type SchemaSnapshot = { database_id: string; driver: Driver; fingerprint: string; schemas: Schema[] };
export type DataColumn = { name: string; data_type: string; encoding: Encoding };
export type TableData = { columns: DataColumn[]; rows: JSONValue[][]; order_stable: boolean };

export type MigrationDraft = {
  id: string; database_id: string; title: string; sql: string; revision: number;
  expected_version: number; source_fingerprint: string | null; applied_at: string | null;
  created_by: string; updated_by: string; created_at: string; updated_at: string;
};
export type Actor = { id: string; username: string };
export type RunError = { code: ErrorCode | string; message: string };
export type MigrationRun = {
  id: string; database_id: string; draft_id: string; draft_revision: number;
  candidate_version: number; version: number | null; sql: string; sql_hash: string;
  actor: Actor; status: RunStatus; started_at: string; finished_at: string | null;
  error: RunError | null;
};
export type MigrationHead = { version: number; last_run_id: string | null };

export type DiffChange = { kind: DiffChangeKind; object: string; destructive: boolean };
export type DiffResult = {
  driver: Driver; expected_version: number; source_fingerprint: string; desired_fingerprint: string;
  changes: DiffChange[]; candidate_sql: string; warnings: string[]; executable: boolean;
};

export type PlanNode = {
  id: string; parent_id: string | null; operation: string; relation: string | null; index: string | null;
  estimated_rows: number | null; estimated_cost: number | null;
  actual_time_ms: number | null; actual_rows: number | null;
};
export type PlanResult = {
  driver: Driver; mode: ExplainMode; nodes: PlanNode[]; raw: JSONValue; warnings: string[];
};
