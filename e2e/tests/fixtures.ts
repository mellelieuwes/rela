import {
  test as base,
  type Page,
  type APIRequestContext,
} from "@playwright/test";
import { spawn, type ChildProcess, execFileSync } from "child_process";
import * as path from "path";
import * as fs from "fs";
import * as os from "os";
import * as net from "net";

const PROJECT_ROOT = path.resolve(__dirname, "../..");
const SERVER_BINARY = path.join(PROJECT_ROOT, "bin", "rela-server");
// The PostgreSQL-backed server (go build -tags postgres ./cmd/rela-server). Used
// only by the opt-in `postgresTest` fixture for specs that exercise pgstore-only
// features (relation/entity content history). Built to a distinct path so it
// never collides with the default fsstore binary.
const PG_SERVER_BINARY = path.join(PROJECT_ROOT, "bin", "rela-server-postgres");

// RELA_E2E_DATABASE_URL is the admin DSN for the postgres-backed e2e specs (a
// database the runner may CREATE/DROP schemas in). When unset, postgresTest specs
// SKIP — mirroring the pgstore Go suite's RELA_TEST_DATABASE_URL gating, so the
// default fsstore e2e run and any environment without a database stay green.
const PG_ADMIN_DSN = process.env.RELA_E2E_DATABASE_URL ?? "";
export const POSTGRES_E2E_ENABLED = PG_ADMIN_DSN !== "";

let pgSchemaCounter = 0;

/** Run a SQL statement against the admin DSN via psql (no node pg dependency).
 *  psql is present in CI's postgres service and in local Postgres installs. */
function psqlExec(sql: string): void {
  execFileSync("psql", [PG_ADMIN_DSN, "-v", "ON_ERROR_STOP=1", "-c", sql], {
    stdio: ["ignore", "ignore", "pipe"],
  });
}

/** Create a fresh isolated schema and return its name. Each postgres-backed test
 *  gets its own schema so runs never cross-contaminate (mirrors the pgstore Go
 *  conformance harness's per-schema isolation). Uniqueness rests on Playwright
 *  running each worker as a SEPARATE OS PROCESS: `process.pid` disambiguates
 *  workers and the per-process counter disambiguates tests within a worker. */
function createPgSchema(): string {
  const schema = `relae2e_${process.pid}_${++pgSchemaCounter}`;
  psqlExec(`CREATE SCHEMA "${schema}"`);
  return schema;
}

function dropPgSchema(schema: string): void {
  try {
    psqlExec(`DROP SCHEMA "${schema}" CASCADE`);
  } catch (e) {
    console.warn(`Failed to drop e2e schema ${schema}: ${String(e)}`);
  }
}

/** Build the server's RELA_DATABASE_URL from the admin DSN, pinned to `schema`
 *  via search_path (keeping public for the pg_trgm extension). The postgres
 *  server auto-migrates the schema on first open. NOTE: this OVERRIDES any
 *  pre-existing `search_path` param in the admin DSN — the e2e admin DSN is
 *  not expected to carry one. */
export function pgDsnForSchema(schema: string): string {
  const u = new URL(PG_ADMIN_DSN);
  // Pin the schema with a plain `search_path` parameter rather than libpq's
  // `options=-c search_path=...`. The latter has to survive URLSearchParams,
  // which writes its space as "+" — and a URI query means a literal plus
  // there, so the server read the parameter name as "+search_path" and
  // refused every connection with SQLSTATE 42704. `search_path` as its own
  // parameter has no space to get wrong.
  u.searchParams.set("search_path", `${schema},public`);
  return u.toString();
}
// Resolve symlinks once — macOS $TMPDIR is typically /var/folders/... which
// canonicalises to /private/var/folders/.... Tests that compare paths want
// the canonical form. (review-response RR-F3IA3)
const TMPDIR = fs.realpathSync(os.tmpdir());

/**
 * Constants derived from the inline test project below. Specs should import
 * these instead of hardcoding strings that couple them to schema values —
 * e.g. use `STATUS.feature.draft` instead of `'draft'`. (RR-F5P1L, RR-M0099)
 */
export const STATUS = {
  feature: {
    draft: "draft",
    approved: "approved",
    in_progress: "in_progress",
    done: "done",
  },
} as const;

export const SEVERITY = {
  low: "low",
  medium: "medium",
  high: "high",
  critical: "critical",
} as const;

export const PRIORITY = {
  low: "low",
  medium: "medium",
  high: "high",
} as const;

/** The metamodel analysis check types the backend emits (via runAnalysis()
 *  in internal/dataentry/analyze.go) and the SPA renders as check-cards at
 *  /analyze. Order matters: Playwright's `toContainText([...])` matches
 *  array entries in sequence, and `expectCheckCardCount` asserts the count.
 *  Keep in lockstep with runAnalysis() and with CHECK_TYPES in
 *  frontend/src/views/AnalyzeView.vue. The Go-side test
 *  TestRunAnalysisSectionNames pins the wire contract. */
export const ANALYSIS_CHECKS = [
  "Properties",
  "Cardinality",
  "Validations",
  "Orphans",
  "Duplicates",
  "ID Gaps",
] as const;

/** Seed entity IDs present in every fresh test project. Assumes the inline
 *  seed data below, in insertion order. Import from specs instead of
 *  writing 'FEAT-001' / 'BUG-001' strings directly. */
export const SEED = {
  features: {
    authentication: "FEAT-001",
    dashboardAnalytics: "FEAT-002",
    exportData: "FEAT-003",
    /** Body content contains two GFM checkboxes (one unchecked, one checked).
     *  Used by checkboxes.spec.ts to exercise the toggle UI. */
    checkboxBody: "FEAT-004",
  },
  bugs: {
    loginFormValidation: "BUG-001",
    memoryLeak: "BUG-002",
  },
  tasks: {
    writeUnitTests: "TASK-001",
    /** Unlinked task. Used by reverse-relations.spec.ts as a candidate for
     *  the non-cards "Implemented by" picker on FEAT-001 — the existing
     *  TASK-001 already implements FEAT-001, so we need a second task that
     *  the add-test can pick without colliding. */
    refactorAuth: "TASK-002",
  },
} as const;

export interface EntityResponse {
  id: string;
  type: string;
  properties: Record<string, unknown>;
  /** Server emits outgoing relations as a flat IDs-only map on GET responses
   *  (the readback shape). The write-side accepts only the JSON:API §9
   *  wrapper — see ModernRelationsField below. */
  relations?: Record<string, string[]>;
  /** Set when fsstore could not read the file (today: git-crypt
   *  encrypted, no key locally). Each entry names a schema property —
   *  or the special "content" sentinel — that exists but is unreadable. */
  inaccessible?: Array<{ name: string; reason: string }>;
  /** Per-`file`-property attachment metadata (TKT-7K3BJF/TKT-Q85275). Present
   *  on a per-entity GET for every file property that holds a file. */
  _attachments?: Record<
    string,
    Array<{ id: string; filename: string; size: number; contentType: string; href: string }>
  >;
}

/** Modern JSON:API §9-style relations body for create/update. The
 *  server rejects the legacy IDs-only form (`["id-1", "id-2"]`) with a
 *  400 `legacy_shape_unsupported`. */
export type ModernRelationsField = Record<
  string,
  { data: Array<{ type: string; id: string; meta?: Record<string, unknown> }> }
>;

export interface PaginatedResponse<T = EntityResponse> {
  data: T[];
  meta: { total: number; page: number; per_page: number; has_more: boolean };
}

export interface ApiHelpers {
  createEntity(
    plural: string,
    data: {
      properties: Record<string, unknown>;
      content?: string;
      relations?: ModernRelationsField;
      id?: string;
      prefix?: string;
    },
  ): Promise<EntityResponse>;
  getEntity(plural: string, id: string): Promise<EntityResponse>;
  /** Both directions, grouped by wire key (inverse names for incoming). */
  getAllRelations(
    plural: string,
    id: string,
  ): Promise<Record<string, Array<{ id: string; type: string; direction?: string }>>>;
  updateEntity(
    plural: string,
    id: string,
    properties: Record<string, unknown>,
  ): Promise<EntityResponse>;
  deleteEntity(plural: string, id: string): Promise<void>;
  listEntities(plural: string, query?: string): Promise<PaginatedResponse>;
  /** Replace an entity's markdown BODY. Separate from updateEntity, which
   *  carries only properties. */
  setEntityContent(plural: string, id: string, content: string): Promise<EntityResponse>;
  /** A target's comment thread (TKT-FIO205). Comments live outside the entity
   *  store, so seeding and cleanup do not ride entity CRUD. */
  listComments(type: string, id: string): Promise<{ comments: { id: string; resolved: boolean }[] }>;
  deleteComment(type: string, id: string, commentId: string): Promise<void>;
  createRelation(
    fromPlural: string,
    fromId: string,
    relation: string,
    toId: string,
  ): Promise<void>;
  /** Create-or-update a relation edge's meta (relation-property values) via a
   *  modern relations PATCH on the FROM entity. Upserts, so calling it repeatedly
   *  with different meta produces successive relation states — used to drive
   *  relation version capture in the relation-history e2e. */
  setRelationMeta(
    fromPlural: string,
    fromId: string,
    relation: string,
    toType: string,
    toId: string,
    meta: Record<string, unknown>,
  ): Promise<void>;
  /** Poll the relation-history timeline until it has at least `count` versions.
   *  Relation versioning is pgstore-only and create/update capture is async (the
   *  reconciliation sweep), so seed-then-assert flows must wait for capture.
   *  `fromType` is the singular entity type (the _relation_history path needs it);
   *  passed explicitly rather than derived, so a new type never silently 404s. */
  waitForRelationVersions(
    fromType: string,
    fromId: string,
    relation: string,
    toId: string,
    count: number,
    options?: { timeout?: number },
  ): Promise<void>;
  /** Poll the entity-history timeline until it has at least `count` versions.
   *  The entity counterpart of waitForRelationVersions — same async-sweep
   *  caveat, same pgstore-only restriction. `type` is the singular entity type
   *  the _history path expects (e.g. "feature", not "features"). */
  waitForEntityVersions(
    type: string,
    id: string,
    count: number,
    options?: { timeout?: number },
  ): Promise<void>;
  /** Returns the current set of outgoing relation edges of the given type for an entity.
   *  Each edge exposes `id` (target entity) and `meta` (relation-property values). */
  listRelations(
    plural: string,
    id: string,
    relation: string,
  ): Promise<Array<{ id: string; meta?: Record<string, unknown> }>>;
  /** Returns the markdown body of an entity, or "" if the entity has no body. */
  getContent(plural: string, id: string): Promise<string>;
  rawRequest(
    method: string,
    path: string,
    data?: unknown,
  ): Promise<import("@playwright/test").APIResponse>;
  /** Wait for an entity to appear in the search index. Use before
   *  navigating to a view that reads via /_search (dashboard, search). */
  waitForIndexed(id: string, options?: { timeout?: number }): Promise<void>;
}

export interface TestFixtures {
  testProject: string;
  serverUrl: string;
  appPage: Page;
  api: ApiHelpers;
}

export interface WorkerFixtures {
  serverBinary: string;
}

/** Find a free ephemeral port on loopback. Note: between this call and the
 *  child binding the port, the kernel may reassign it elsewhere under load —
 *  callers MUST retry on startup failure (see spawnServer). */
async function findFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.listen(0, "127.0.0.1", () => {
      const addr = server.address();
      if (addr && typeof addr === "object") {
        const port = addr.port;
        server.close(() => resolve(port));
      } else {
        reject(new Error("Could not get port"));
      }
    });
    server.on("error", reject);
  });
}

/** Probe a known-live API endpoint with the same Origin header the tests will
 *  use. The root path serves the SPA static bundle and is ready before the API
 *  handlers are wired, which is why we target /api/v1/_config specifically
 *  (see RR-LWG6W). The backend's security middleware otherwise rejects
 *  Origin-less probes with 403 origin_missing, so we always send Origin. */
async function waitForServer(
  url: string,
  origin: string,
  timeout = 30000,
): Promise<void> {
  const start = Date.now();
  const probeUrl = `${url}/api/v1/_config`;
  while (Date.now() - start < timeout) {
    try {
      const r = await fetch(probeUrl, { headers: { Origin: origin } });
      if (r.ok) return;
    } catch {
      // not ready
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Server at ${probeUrl} did not start within ${timeout}ms`);
}

/** Wait for a child process to actually exit. SIGTERM is async; a bare
 *  proc.kill() returns immediately and the kernel may hold the socket past the
 *  next test start. See RR-17XTS. */
async function waitForExit(
  proc: ChildProcess,
  signal: NodeJS.Signals = "SIGTERM",
): Promise<void> {
  if (proc.exitCode !== null || proc.signalCode !== null) return;
  await new Promise<void>((resolve) => {
    const done = () => resolve();
    proc.once("exit", done);
    proc.kill(signal);
    // Escalate to SIGKILL if the server refuses to die within 5s
    setTimeout(() => {
      if (proc.exitCode === null && proc.signalCode === null) {
        proc.kill("SIGKILL");
      }
    }, 5000);
  });
}

/** Build a Go binary if missing, using a filesystem lock so concurrent workers
 *  don't race each other writing the same output file (RR-BZUH5). In CI the
 *  fixture should never fire because the binary is pre-built in a prior step;
 *  we fail loudly there to surface misconfiguration. */
function buildIfMissing(binaryPath: string, ...buildArgs: string[]): string {
  if (fs.existsSync(binaryPath)) return binaryPath;
  if (process.env.CI) {
    throw new Error(
      `Missing ${binaryPath} in CI. The e2e CI job is expected to build it in a prior step; ` +
        "do not fall back to in-fixture builds on CI.",
    );
  }

  const lockPath = `${binaryPath}.lock`;
  const start = Date.now();
  // Try to acquire an exclusive lock. Another worker may already be building.
  let acquired = false;
  while (!acquired) {
    try {
      fs.mkdirSync(lockPath);
      acquired = true;
    } catch (e) {
      const code = (e as NodeJS.ErrnoException).code;
      if (code !== "EEXIST") throw e;
      if (fs.existsSync(binaryPath)) return binaryPath;
      if (Date.now() - start > 300_000) {
        throw new Error(
          `Build lock ${lockPath} held > 5 min; something is stuck.`,
          { cause: e },
        );
      }
      // busy-wait 250ms; builds take seconds
      const end = Date.now() + 250;
      while (Date.now() < end) {
        // naive sync wait — this fixture runs once per worker only
      }
    }
  }
  try {
    console.log(`Building ${path.basename(binaryPath)}...`);
    // execFileSync with an args array: no shell, so the absolute paths can't
    // be interpreted as shell syntax (CodeQL js/shell-command-injection-from-
    // environment; also just breaks on checkout paths containing spaces).
    execFileSync("go", ["build", ...buildArgs, "-o", binaryPath], {
      cwd: PROJECT_ROOT,
      stdio: "inherit",
    });
  } finally {
    fs.rmdirSync(lockPath);
  }
  return binaryPath;
}

function createTestProject(): string {
  const tmpDir = fs.mkdtempSync(path.join(TMPDIR, "rela-e2e-"));
  fs.writeFileSync(path.join(tmpDir, "schema.yaml"), METAMODEL_YAML);
  fs.writeFileSync(path.join(tmpDir, "data-entry.yaml"), DATA_ENTRY_YAML);
  fs.mkdirSync(path.join(tmpDir, "entities", "features"), { recursive: true });
  fs.mkdirSync(path.join(tmpDir, "entities", "bugs"), { recursive: true });
  fs.mkdirSync(path.join(tmpDir, "entities", "tasks"), { recursive: true });
  fs.mkdirSync(path.join(tmpDir, "relations"), { recursive: true });
  fs.mkdirSync(path.join(tmpDir, "templates", "entities"), { recursive: true });
  fs.mkdirSync(path.join(tmpDir, "scripts", "docs"), { recursive: true });
  fs.mkdirSync(path.join(tmpDir, "apps", "e2e-demo"), { recursive: true });
  fs.writeFileSync(
    path.join(tmpDir, "apps", "e2e-demo", "index.html"),
    E2E_DEMO_APP_HTML,
  );
  fs.writeFileSync(
    path.join(tmpDir, "apps", "e2e-demo", "app.js"),
    E2E_DEMO_APP_JS,
  );
  for (const [rel, content] of Object.entries(SEED_ENTITIES)) {
    fs.writeFileSync(path.join(tmpDir, rel), content);
  }
  for (const [rel, content] of Object.entries(SEED_TEMPLATES)) {
    fs.writeFileSync(path.join(tmpDir, rel), content);
  }
  for (const [rel, content] of Object.entries(SEED_DOCS)) {
    fs.writeFileSync(path.join(tmpDir, rel), content);
  }
  return tmpDir;
}

/** Spawn rela-server on a free port with retry-on-startup-failure: the port
 *  we pick from findFreePort may have been reassigned by the kernel before the
 *  child binds it, under load (RR-B8GJT). Up to 3 attempts.
 *
 *  A server that DIES during startup fails fast rather than waiting out
 *  waitForServer's timeout, and its stderr travels in the thrown error. Both
 *  matter: without them a server that refused to start (a bad DSN, a failed
 *  migration) surfaced only as "Test timeout of 30000ms exceeded while setting
 *  up serverUrl", with the real reason — which the child had already printed —
 *  discarded. A spawn failure is also an 'error' EVENT, not a throw; with no
 *  listener Node re-raises it as an unhandled error out of band, which is how
 *  a failed startup came to be reported as a bare, and entirely misleading,
 *  `spawn ... ENOENT`. (BUG-YJEIFH) */
async function spawnServer(
  serverBinary: string,
  cwd: string,
  extraEnv: NodeJS.ProcessEnv = {},
): Promise<{ proc: ChildProcess; url: string; logs: () => string }> {
  const attempts = 3;
  let lastErr: unknown;
  for (let i = 0; i < attempts; i++) {
    const port = await findFreePort();
    const url = `http://localhost:${port}`;
    // Reachability coverage: when RELA_E2E_COVERDIR is set, serverBinary is an
    // instrumented (`go build -cover`) build that writes Go coverage data on a
    // clean exit (the server's SIGTERM handler drains and exits gracefully).
    // Give each spawned server its own GOCOVERDIR subdir so parallel workers
    // never share a partial write; `go tool covdata` merges them afterwards.
    const spawnEnv: NodeJS.ProcessEnv = { ...process.env, ...extraEnv };
    const coverRoot = process.env.RELA_E2E_COVERDIR;
    if (coverRoot) {
      const covDir = path.join(coverRoot, `srv-${port}-${Date.now()}`);
      fs.mkdirSync(covDir, { recursive: true });
      spawnEnv.GOCOVERDIR = covDir;
    }
    const proc: ChildProcess = spawn(
      serverBinary,
      ["-port", String(port), "-allowed-origin", url],
      { cwd, env: spawnEnv, stdio: ["ignore", "pipe", "pipe"] },
    );
    let stdout = "";
    let stderr = "";
    proc.stdout?.on("data", (d) => (stdout += d.toString()));
    proc.stderr?.on("data", (d) => (stderr += d.toString()));

    // Resolves as soon as the child is known to be unusable, so a server that
    // exits immediately is not waited out for the full readiness timeout.
    const died = new Promise<never>((_, reject) => {
      proc.once("error", (e) =>
        reject(new Error(`could not spawn ${serverBinary}: ${String(e)}`)),
      );
      proc.once("exit", (code, signal) =>
        reject(
          new Error(
            `server exited during startup (code ${code}, signal ${signal})\n` +
              `--- stderr ---\n${stderr.trim() || "(empty)"}`,
          ),
        ),
      );
    });
    // The rejection is always consumed below, but only once the race settles;
    // mark it handled now so a loser never trips unhandledRejection.
    died.catch(() => {});

    try {
      await Promise.race([waitForServer(url, url), died]);
      return {
        proc,
        url,
        logs: () => `--- stdout ---\n${stdout}\n--- stderr ---\n${stderr}\n`,
      };
    } catch (e) {
      lastErr = e;
      await waitForExit(proc);
    }
  }
  throw new Error(
    `Failed to start rela-server after ${attempts} attempts: ${String(lastErr)}`,
  );
}

export const test = base.extend<TestFixtures, WorkerFixtures>({
  serverBinary: [
    // eslint-disable-next-line no-empty-pattern
    async ({}, use) => {
      await use(buildIfMissing(SERVER_BINARY, "./cmd/rela-server"));
    },
    { scope: "worker" },
  ],

  // eslint-disable-next-line no-empty-pattern
  testProject: async ({}, use) => {
    const dir = createTestProject();
    await use(dir);
    try {
      fs.rmSync(dir, { recursive: true, force: true });
    } catch (e) {
      // Real cleanup failures likely indicate a process-still-holding-files
      // bug — warn rather than silently accumulating /tmp detritus.
      // (review-response RR-3DJ2C)
      console.warn(`Failed to remove temp project ${dir}: ${String(e)}`);
    }
  },

  serverUrl: async ({ testProject, serverBinary }, use, testInfo) => {
    // A resolvable identity. Most specs do not care, but comments REFUSE an
    // unstamped principal rather than recording "unknown" (TKT-FIO205): a
    // comment nobody is recorded as writing can never satisfy an *-own
    // permission check, so it could be neither edited nor deleted.
    const { proc, url, logs } = await spawnServer(serverBinary, testProject, {
      RELA_DATAENTRY_USER: "e2e@example.com",
    });
    try {
      await use(url);
    } finally {
      if (testInfo.status !== testInfo.expectedStatus) {
        // Attach server logs to the failing test so a CI reader can correlate
        // a 500 with what the backend was saying. (review-response RR-J9BIT)
        await testInfo.attach("rela-server.log", {
          body: logs(),
          contentType: "text/plain",
        });
      }
      await waitForExit(proc);
    }
  },

  appPage: async ({ browser, serverUrl }, use, testInfo) => {
    const context = await browser.newContext({
      extraHTTPHeaders: { Origin: serverUrl },
    });
    const page = await context.newPage();

    // Self-contained-binary guard (TKT-ZDRS): rela-server embeds the SPA via
    // //go:embed and is meant to run without third-party network access.
    // Capture every off-origin http(s) request the browser makes during a
    // test; if any fire, fail the test in afterEach with the offending URLs.
    // This catches both the original EasyMDE-FontAwesome regression and any
    // future "let me just grab this CDN script" mistake across the whole SPA.
    const allowedOrigin = new URL(serverUrl).origin;
    const offOriginRequests: string[] = [];
    page.on("request", (req) => {
      const url = req.url();
      if (!/^https?:/i.test(url)) return; // data:, blob:, file: are local
      let origin: string;
      try {
        origin = new URL(url).origin;
      } catch {
        // Malformed enough that URL() throws — surface it instead of silently
        // classifying as same-origin.
        offOriginRequests.push(`<unparseable> ${url}`);
        return;
      }
      if (origin !== allowedOrigin) offOriginRequests.push(url);
    });

    // Vue-error guard (issue #997). Errors thrown inside a Vue lifecycle
    // hook / render / watcher — notably the route-driven unmount crash that
    // motivated this guard — are swallowed by the framework: they never reach
    // `pageerror` or surface as a thrown exception. They DO reach the SPA's
    // global `app.config.errorHandler` (see frontend/src/main.ts), which logs
    // a `[vue-error] ...` line to console.error. We capture those here and
    // fail the test in afterEach, so EVERY navigation in EVERY spec is an
    // unmount-error probe for free — not just the dedicated regression test.
    const vueErrors: string[] = [];
    page.on("console", (msg) => {
      if (msg.type() !== "error") return;
      const text = msg.text();
      if (text.startsWith("[vue-error]")) vueErrors.push(text);
    });

    await page.goto(`${serverUrl}/`);
    await use(page);
    await context.close();

    if (vueErrors.length > 0) {
      await testInfo.attach("vue-errors", {
        body: vueErrors.join("\n\n"),
        contentType: "text/plain",
      });
      throw new Error(
        `The SPA's Vue error handler fired ${vueErrors.length} time(s) during this test ` +
          `(framework-swallowed errors — e.g. a lifecycle/unmount crash, see issue #997): ` +
          `${vueErrors[0].split("\n")[0]}` +
          (vueErrors.length > 1
            ? ` (+${vueErrors.length - 1} more — see attachment)`
            : ""),
      );
    }

    if (offOriginRequests.length > 0) {
      await testInfo.attach("off-origin-requests", {
        body: offOriginRequests.join("\n"),
        contentType: "text/plain",
      });
      throw new Error(
        `rela-server is meant to be self-contained but the SPA fetched ${offOriginRequests.length} ` +
          `off-origin URL(s): ${offOriginRequests.slice(0, 5).join(", ")}` +
          (offOriginRequests.length > 5
            ? ` (+${offOriginRequests.length - 5} more — see attachment)`
            : ""),
      );
    }
  },

  api: async ({ serverUrl, appPage }, use) => {
    const request: APIRequestContext = appPage.request;

    /** Poll a `_history` / `_relation_history` listing until it reports at
     *  least `count` versions. Shared by the entity and relation waiters —
     *  both face the same async reconciliation sweep, so a seed-then-assert
     *  flow has to wait for capture rather than read straight after writing.
     *  `subject` only labels the timeout error. */
    async function pollForVersions(
      get: typeof call,
      apiPath: string,
      count: number,
      subject: string,
      options?: { timeout?: number },
    ): Promise<void> {
      const timeout = options?.timeout ?? 20000;
      const start = Date.now();
      while (Date.now() - start < timeout) {
        const resp = await get("GET", apiPath).catch(() => null);
        if (resp && resp.ok()) {
          const body = (await resp.json()) as { versions?: unknown[] };
          if ((body.versions?.length ?? 0) >= count) return;
        }
        await new Promise((r) => setTimeout(r, 200));
      }
      throw new Error(
        `${subject} did not reach ${count} version(s) within ${timeout}ms`,
      );
    }

    async function call(method: string, apiPath: string, data?: unknown) {
      const options: Record<string, unknown> = {
        method,
        // Redundant because the context already carries Origin, but keeps
        // the intent visible for readers auditing a test's API calls.
        headers: { Origin: serverUrl },
      };
      if (data !== undefined) options.data = data;
      const resp = await request.fetch(
        `${serverUrl}/api/v1/${apiPath}`,
        options,
      );
      if (!resp.ok()) {
        throw new Error(
          `${method} /api/v1/${apiPath} → ${resp.status()}: ${await resp.text()}`,
        );
      }
      return resp;
    }

    await use({
      async createEntity(plural, data) {
        return (await call("POST", plural, data)).json();
      },
      async getEntity(plural, id) {
        return (await call("GET", `${plural}/${id}`)).json();
      },
      async getAllRelations(plural, id) {
        // Both directions, grouped by wire key. A per-entity GET carries
        // OUTGOING edges only, so an incoming edge can only be verified here.
        return (await call("GET", `${plural}/${id}/relations`)).json();
      },
      async updateEntity(plural, id, properties) {
        return (await call("PATCH", `${plural}/${id}`, { properties })).json();
      },
      async deleteEntity(plural, id) {
        // Fails loudly on error. Callers doing cleanup should append
        // `.catch(() => {})` explicitly. (RR-MS1FM)
        await call("DELETE", `${plural}/${id}`);
      },
      async listEntities(plural, query) {
        const p = query ? `${plural}?${query}` : plural;
        return (await call("GET", p)).json();
      },
      async setEntityContent(plural, id, content) {
        // Separate from updateEntity, which sends only `properties`: a comment
        // spec needs to rewrite the BODY to exercise anchor drift.
        return (await call("PATCH", `${plural}/${id}`, { content })).json();
      },
      // Comments (TKT-FIO205) live outside the entity store, so seeding and
      // cleanup need their own helpers rather than riding entity CRUD.
      async listComments(type, id) {
        return (await call("GET", `_comments/${type}/${id}`)).json();
      },
      async deleteComment(type, id, commentId) {
        await call("DELETE", `_comments/${type}/${id}/${commentId}`);
      },
      async setRelationMeta(fromPlural, fromId, relation, toType, toId, meta) {
        // Modern JSON:API relations body: upsert the edge with its meta. A PATCH
        // that includes the edge again with new meta updates it in place.
        await call("PATCH", `${fromPlural}/${fromId}`, {
          relations: {
            [relation]: { data: [{ type: toType, id: toId, meta }] },
          },
        });
      },
      async createRelation(fromPlural, fromId, relation, toId) {
        await call("POST", `${fromPlural}/${fromId}/relations/${relation}`, {
          id: toId,
        });
      },
      async waitForRelationVersions(fromType, fromId, relation, toId, count, options) {
        await pollForVersions(
          call,
          `_relation_history/${encodeURIComponent(fromType)}/${encodeURIComponent(fromId)}` +
            `/${encodeURIComponent(relation)}/${encodeURIComponent(toId)}`,
          count,
          `relation ${fromId}--${relation}--${toId}`,
          options,
        );
      },
      async waitForEntityVersions(type, id, count, options) {
        await pollForVersions(
          call,
          `_history/${encodeURIComponent(type)}/${encodeURIComponent(id)}`,
          count,
          `entity ${id}`,
          options,
        );
      },
      async listRelations(plural, id, relation) {
        const resp = await call("GET", `${plural}/${id}/relations/${relation}`);
        return (await resp.json()) as Array<{
          id: string;
          meta?: Record<string, unknown>;
        }>;
      },
      async getContent(plural, id) {
        const resp = await call("GET", `${plural}/${id}`);
        const body = (await resp.json()) as { content?: string };
        return body.content ?? "";
      },
      async rawRequest(method, apiPath, data) {
        return call(method, apiPath, data);
      },
      async waitForIndexed(id, options) {
        // Searches ALL indexed-entity channels that a dashboard card might
        // use: _search (bleve), and a fallback direct GET which at least
        // confirms the store has the entity even if the index lags.
        const timeout = options?.timeout ?? 5000;
        const start = Date.now();
        // Infer plural from id prefix. Keep this narrow — the inline project
        // only has three entity types.
        const prefix = id.split("-")[0];
        const pluralByPrefix: Record<string, string> = {
          FEAT: "features",
          BUG: "bugs",
          TASK: "tasks",
        };
        const plural = pluralByPrefix[prefix];
        if (!plural) {
          throw new Error(`waitForIndexed: unknown ID prefix for ${id}`);
        }
        while (Date.now() - start < timeout) {
          const resp = await call("GET", `${plural}/${id}`).catch(() => null);
          if (resp && resp.ok()) return;
          await new Promise((r) => setTimeout(r, 100));
        }
        throw new Error(
          `entity ${id} not reachable via GET /${plural}/${id} within ${timeout}ms`,
        );
      },
    });
  },
});

/**
 * postgresTest is the opt-in fixture for specs that need pgstore-only features
 * (entity/relation content history). It spawns `rela-server-postgres` against a
 * per-test isolated schema instead of the default fsstore server; `appPage` and
 * `api` are inherited unchanged and transparently talk to the postgres backend
 * via the overridden `serverUrl`.
 *
 * Specs MUST guard with `postgresTest.skip(!POSTGRES_E2E_ENABLED, ...)` (or the
 * describe-level equivalent) so they skip cleanly when RELA_E2E_DATABASE_URL is
 * unset — the default e2e run has no database.
 */
export const postgresTest = test.extend<
  { pgSchema: string },
  { serverBinary: string }
>({
  // Worker-scoped: the postgres server binary (built by CI, or on demand locally).
  serverBinary: [
    // eslint-disable-next-line no-empty-pattern
    async ({}, use) => {
      await use(
        buildIfMissing(
          PG_SERVER_BINARY,
          "-tags",
          "postgres",
          "./cmd/rela-server",
        ),
      );
    },
    { scope: "worker" },
  ],

  // A fresh isolated schema per test, dropped afterwards.
  // eslint-disable-next-line no-empty-pattern
  pgSchema: async ({}, use) => {
    const schema = createPgSchema();
    try {
      await use(schema);
    } finally {
      dropPgSchema(schema);
    }
  },

  // Override serverUrl to spawn the postgres server with a schema-pinned DSN.
  serverUrl: async (
    { testProject, serverBinary, pgSchema },
    use,
    testInfo,
  ) => {
    const { proc, url, logs } = await spawnServer(serverBinary, testProject, {
      RELA_DATABASE_URL: pgDsnForSchema(pgSchema),
    });
    try {
      await use(url);
    } finally {
      if (testInfo.status !== testInfo.expectedStatus) {
        await testInfo.attach("rela-server-postgres.log", {
          body: logs(),
          contentType: "text/plain",
        });
      }
      await waitForExit(proc);
    }
  },
});

export { expect } from "@playwright/test";

// ---- Test project fixture data ----
//
// The inline metamodel is deliberately minimal: feature/bug/task with a
// handful of properties. No automations, no validation rules. Tests that
// need those will have to add them here; do NOT point the fixture at the
// dogfood `tickets/` project (it's load-bearing real data, not a fixture).
// (review-response RR-GX4BK)

const METAMODEL_YAML = `
version: "1.0"

types:
  feature_status:
    values: [draft, approved, in_progress, done]
    default: draft
  severity:
    values: [low, medium, high, critical]
    default: medium
  priority:
    values: [low, medium, high]
    default: medium

entities:
  feature:
    label: Feature
    id_type: sequential
    id_prefix: FEAT
    properties:
      title:
        type: string
        required: true
      status:
        type: feature_status
      description:
        type: string
      priority:
        type: priority
    # TKT-PEKL8L: backs the sidebar entities group that sidebar-entities.spec.ts
    # adds. No default scope, so no other list or view is narrowed by it.
    query_scopes:
      in_flight: "entity.status == 'in_progress'"

  bug:
    label: Bug
    id_type: sequential
    id_prefix: BUG
    properties:
      title:
        type: string
        required: true
      severity:
        type: severity
      status:
        type: feature_status
      priority:
        type: priority
      description:
        type: string
      # TKT-7K3BJF: file properties for create-time attachment staging.
      # 'screenshot' is single-cap (replace semantics), 'evidence' is
      # multi-cap so the staged widget's capacity logic is exercised.
      screenshot:
        type: file
      evidence:
        type: file
        max: 3

  # BUG-L1DHC5: a dedicated type whose file property is REQUIRED. Separate from
  # 'bug' on purpose — a required property on a shared type makes every other
  # test's entity emit a required_property_unset warning.
  signoff:
    label: Sign-off
    id_type: sequential
    id_prefix: SIGN
    properties:
      title:
        type: string
        required: true
      document:
        type: file
        required: true

  task:
    label: Task
    id_type: sequential
    id_prefix: TASK
    properties:
      title:
        type: string
        required: true
      status:
        type: feature_status
      assignee:
        type: string
      done:
        type: boolean
      # BUG-FB0LN8: lets a fixture hide a default-policy field alongside a
      # clear-policy one in a single pass.
      note:
        type: string

  # TKT-E7NNM fixtures: covers manual-ID, multi-prefix, and the combinations
  # we want to exercise in forms.spec.ts. Keep names short and orthogonal
  # so the inline metamodel stays readable.
  tag:
    label: Tag
    id_type: manual
    properties:
      name:
        type: string
        required: true

  decision:
    label: Decision
    id_type: short
    id_prefixes: [DEC-, ADR-]
    properties:
      title:
        type: string
        required: true

  module:
    label: Module
    id_type: manual
    id_prefix: MOD-
    properties:
      name:
        type: string
        required: true

  specification:
    label: Specification
    id_type: manual
    id_prefixes: [SPEC-, RFC-]
    properties:
      name:
        type: string
        required: true

relations:
  blocks:
    from: [feature, bug, task]
    to: [feature, bug, task]
    inverse: blockedBy
    properties:
      reason:
        type: string
        required: true
        description: Why this entity blocks the other
      severity:
        type: severity
      resolved_date:
        type: date
      impact_score:
        type: integer
      is_workaround_available:
        type: boolean
  tagged:
    from: [feature]
    to: [feature]
    inverse: tagged_by
    properties:
      added_by:
        type: string
      added_date:
        type: date
  implements:
    from: [task]
    to: [feature]
    inverse: implementedBy
  fixes:
    from: [task]
    to: [bug]
    inverse: fixedBy

# Commenting (TKT-FIO205). Enabled for every type so the comment specs can use
# whichever seed entity is convenient; the ACL is untouched, so the default
# no-policy deployment applies and every principal may comment.
comments:
  enabled: true
  on: ["*"]
`;

const DATA_ENTRY_YAML = `
version: "1.0"

app:
  name: "E2E Test App"
  description: "Test project for Playwright E2E tests"
  # TKT-7K3BJF: a deliberately tiny attachment cap so the create-form failure
  # path (create succeeds, upload 413s) can be driven end-to-end with a small
  # file. Every fixture attachment in the suite is well under 1 KiB.
  max_attachment_bytes: 1024

# Enable dark mode so the theme toggle renders in the status bar. Palette
# config is validated strictly; unknown keys raise startup errors.
palette:
  base: "#ffffff"
  surface: "#fafafa"
  accent: "#0066cc"
  text: "#111111"
  dark:
    base: "#111111"
    surface: "#222222"
    accent: "#4da6ff"
    text: "#eeeeee"

dashboard:
  title: "Dashboard"
  description: "Feature/bug overview"
  cards:
    - title: "Open Features"
      query: "type:feature status:draft"
      display: count
    - title: "In Progress"
      query: "type:feature status:in_progress"
      display: count
    - title: "By Status"
      query: "type:feature"
      display: breakdown
      group_by: status
    - title: "By Priority"
      query: "type:feature"
      display: breakdown
      group_by: priority
    - title: "Critical Issues"
      query: "type:bug prop:severity=critical"
      display: table
      columns:
        - property: title
          link: detail
        - property: status
      sort:
        - property: status
          direction: asc
      limit: 10

forms:
  feature:
    entity_type: feature
    title: "Feature"
    body: true
    fields:
      - property: title
      - property: status
      - property: priority
      - property: description
        widget: textarea
    relations:
      - relation: tagged
        direction: outgoing
        widget: cards
        properties:
          - property: added_by
            label: "Added By"
          - property: added_date
            label: "Added"
      - relation: blocks
        direction: outgoing
        widget: cards
        properties:
          - property: reason
            label: "Block Reason"
          - property: severity
          - property: resolved_date
            label: "Resolved"
          - property: impact_score
            label: "Impact"
          - property: is_workaround_available
            label: "Workaround?"
      - relation: blocks
        direction: incoming
        widget: cards
        properties:
          - property: reason
            label: "Block Reason"
          - property: severity
          - property: resolved_date
            label: "Resolved"
          - property: impact_score
            label: "Impact"
          - property: is_workaround_available
            label: "Workaround?"
      # Non-cards reverse relation: 'implements' is task -> feature, so on
      # the feature form it must be rendered incoming. Used by
      # reverse-relations.spec.ts to exercise RelationPicker with
      # direction: incoming.
      - relation: implements
        direction: incoming
        label: "Implemented by"

  bug:
    entity_type: bug
    title: "Bug"
    fields:
      - property: title
      - property: severity
      - property: status
      - property: priority
      - property: description
        widget: textarea
      # TKT-7K3BJF: exercised by attachments-create.spec.ts.
      - property: screenshot
      - property: evidence

  # BUG-L1DHC5: a form whose file property is REQUIRED. Kept SEPARATE from the
  # 'bug' form on purpose — adding a required field there would block every
  # other bug-creation test in the suite.
  bug_signoff:
    entity_type: signoff
    title: "Sign-off"
    fields:
      - property: title
      - property: document

  task:
    entity_type: task
    title: "Task"
    fields:
      - property: title
      - property: status
      - property: assignee
      - property: done
    relations:
      - relation: implements
        label: Implements Feature
      - relation: fixes
        label: Fixes Bug

  # TKT-7YHKD1: "Create & add another" resets the create form CLEAN, except
  # for fields/relations the operator marked keep_on_add_another. The assignee
  # carries over (the batch context — you are entering ten tasks for one
  # person); title and status do not.
  task_add_another:
    entity_type: task
    title: "Task (add another)"
    fields:
      - property: title
      - property: status
      - property: assignee
        keep_on_add_another: true
    relations:
      # A kept RELATION is the case the docs promote hardest, and it exercises
      # a path a kept property does not: the payload reshape needs the picker's
      # id -> type map, which the reset also has to carry.
      - relation: implements
        label: Implements Feature
        keep_on_add_another: true

  # Wizard (multi-step) task form — exercises steps, visible_when,
  # required_when, next/back, and hidden-branch pruning (wizard.spec.ts).
  task_wizard:
    entity_type: task
    title: "New Task (wizard)"
    steps:
      - title: "Basics"
        fields:
          - property: title
            required: true
          - property: done
            widget: checkbox
      - title: "Assignment"
        # Only shown once the task is marked done.
        visible_when: "form.done == true"
        fields:
          - property: assignee
            required_when: "form.done == true"
        # A relation under a conditional step — must also be pruned when hidden.
        relations:
          - relation: implements
            label: "Implements"
      - title: "Status"
        fields:
          - property: status
            required: true

  # A FLAT (single-page) form that also uses visible_when/required_when — proves
  # conditions work on non-wizard forms after the unification (TKT-ZKGY3).
  task_flat_conditional:
    entity_type: task
    title: "Task (flat, conditional)"
    fields:
      - property: title
        required: true
      - property: done
        widget: checkbox
      - property: assignee
        visible_when: "form.done == true"
        required_when: "form.done == true"

  # BUG-FB0LN8: clear_when_hidden "yes" opts back in to clearing a hidden
  # branch's stored value — the behavior that used to be unconditional. Keeps
  # the old semantics pinned now that the default is to KEEP the value.
  task_clear_when_hidden:
    entity_type: task
    title: "Task (clears on hide)"
    fields:
      - property: title
        required: true
      - property: done
        widget: checkbox
      - property: assignee
        visible_when: "form.done == true"
        clear_when_hidden: "yes"

  # MIXED policies hidden by ONE trigger, via a SELECT. A keep-policy and a
  # clear-policy field hide in the same pass, so each must honor its own
  # setting rather than the batch taking one decision for all of them.
  task_mixed_policies:
    entity_type: task
    title: "Task (mixed clear policies, one trigger)"
    fields:
      - property: title
        required: true
      - property: status
      - property: note
        visible_when: "form.status == 'approved'"
      - property: assignee
        visible_when: "form.status == 'approved'"
      - property: done
        visible_when: "form.status == 'approved'"
        clear_when_hidden: "yes"

  tag:
    entity_type: tag
    title: "Tag"
    fields:
      - property: name

  decision:
    entity_type: decision
    title: "Decision"
    fields:
      - property: title

  module:
    entity_type: module
    title: "Module"
    fields:
      - property: name

  specification:
    entity_type: specification
    title: "Specification"
    fields:
      - property: name

lists:
  features:
    entity_type: feature
    title: "Features"
    columns:
      - property: title
      - property: status
      - property: priority
    create_form: feature
    edit_form: feature
    filter_controls:
      - property: status
      - property: priority
    default_sort:
      - field: title
        direction: asc

  bugs:
    entity_type: bug
    title: "Bugs"
    columns:
      - property: title
      - property: severity
      - property: status
    create_form: bug
    edit_form: bug
    filter_controls:
      - property: severity
      - property: status

  tasks:
    entity_type: task
    title: "Tasks"
    columns:
      - property: title
      - property: status
      - property: assignee
    create_form: task
    edit_form: task
    # Relation filter control (TKT-DL16XM): filter tasks by the feature they
    # implement. 'implements' is task -> feature, so options come from the
    # relation's "to" (feature) entities. Exercised by list.spec.ts.
    filter_controls:
      - relation: implements
        label: "Implements"

# Custom entity-detail views. The task view deliberately renders a
# "display: list" relation section with fields so the row mounts a
# SectionEditForm whose AutoSaveIndicator is inline in the list row. This
# is the exact shape that triggered the navigate-away unmount crash in
# issue #997 (entity-detail-list-unmount.spec.ts). It lives on the task
# type so it doesn't override the default feature detail rendering that
# other specs (e.g. git-crypt.spec.ts) assert against.
views:
  task:
    title: "Task"
    entry:
      type: task
    traverse:
      # TASK-001 implements FEAT-001 in the seed, so this list section
      # renders one populated row with an inline-edit SectionEditForm.
      - from: entry
        follow: implements
        collect_as: implemented
    sections:
      - heading: "Task"
        source: entry
        display: properties
        # Deliberately left at the display default (TKT-HOIX1) so the suite
        # exercises BOTH render modes on one page: this entry section renders
        # display values while the "Implements" list section below is inline-
        # editable. A future spec asserting an editable form here must add
        # render: input rather than assume the pre-TKT-HOIX1 default.
        fields:
          - property: status
          - property: assignee
      # TKT-3R7RF3: a section whose fields opt into inline edit AND override
      # which widget renders them. The done property is a boolean, so its type
      # default is already checkbox; title is the load-bearing case — a string
      # would default to TextWidget, and only a widget override makes it a
      # textarea. Keeps the override honest: if the plumbing silently dropped,
      # the textarea assertion fails even though the checkbox one would pass.
      # (No backticks in this comment: it lives inside a TS template literal.)
      - heading: "Progress"
        source: entry
        display: properties
        render: input
        fields:
          - property: done
            widget: checkbox
          - property: title
            widget: textarea
      # The entry's markdown body. Needed by the comment specs: a text-range
      # anchor has nothing to attach to unless the body actually renders.
      - source: entry
        display: content
      - heading: "Implements"
        source: implemented
        display: list
        # render: input is REQUIRED for this section to mount a
        # SectionEditForm — fields render as display by default (TKT-HOIX1).
        # Without it the #997 unmount-crash guard silently stops exercising
        # the DOM shape it exists to guard.
        fields:
          - property: status
            render: input
          - property: priority
            render: input
        empty_message: "No implemented features"
      # TKT-3CSZRG: a table-display section whose first column carries a
      # link:. Its cells render real anchors, and their click handler must
      # defer modifier/middle clicks to the browser instead of preventing
      # the default. Without this fixture the entity-detail table display had
      # no e2e coverage at all, which is how an unconditional @click.prevent
      # survived on those anchors.
      - heading: "Linked features"
        source: implemented
        display: table
        columns:
          - property: title
            link: detail
          - property: status

kanbans:
  feature-board:
    entity_type: feature
    title: "Feature Board"
    column_property: status
    columns:
      - value: draft
        label: Draft
      - value: approved
        label: Approved
      - value: in_progress
        label: In Progress
      - value: done
        label: Done
    card:
      title: title
      fields:
        - property: priority
    create_form: feature
    edit_form: feature
    filter_controls:
      - property: priority
        label: Priority

  bug-board:
    entity_type: bug
    title: "Bug Board"
    column_property: status
    columns:
      - value: draft
        label: New
      - value: in_progress
        label: In Progress
      - value: done
        label: Fixed
    card:
      title: title
      fields:
        - property: severity
    create_form: bug
    edit_form: bug

documents:
  feature-overview:
    title: "Feature Overview"
    entity_type: feature
    script: docs/feature_overview.lua
  # feature_summary and feature_readonly are wired in for the
  # document-edit-button spec. feature_summary opts into the new edit:
  # block; feature_readonly deliberately doesn't, so we can assert the
  # button is gated on edit being present.
  feature_summary:
    title: "Feature Summary"
    entity_type: feature
    # argv array, no shell (TKT-QGHNVA). {in} is the entry entity's markdown
    # file; its frontmatter carries the id, so cat emits a document naming the
    # entity without the id ever reaching the command line.
    command: ["cat", "{in}"]
    edit:
      # Reusing the shared 'feature' form rather than adding a dedicated
      # edit-mode form: adding feature_edit would shift the form count
      # asserted in settings.spec.ts and the URL pattern asserted in
      # entity-detail.spec.ts (which expects getEditFormId to return
      # 'feature'). The shared form serves both create and edit because
      # DynamicForm flips on entityId presence.
      form: feature
      label: "Edit feature"
  feature_readonly:
    title: "Feature Readonly"
    entity_type: feature
    command: ["cat", "{in}"]

navigation:
  - label: "Dashboard"
    dashboard: true
  - label: "Features"
    list: features
  - label: "Feature Board"
    kanban: feature-board
  - label: "Bugs"
    list: bugs
  - label: "Bug Board"
    kanban: bug-board
  - label: "Tasks"
    list: tasks
  - label: "Search"
    search: true
  - label: "Settings"
    settings: true
  - label: "Analyze"
    analyze: true
  - label: "Conflicts"
    conflicts: true
`;

/** Custom-app HTML used by apps.spec.ts. Exercises the rela bridge end-to-end:
 *  a read (list features) on load, and a relation write (FEAT-001 blocks
 *  FEAT-002) on button click. Results land in stable data-testid nodes the
 *  page object reads. The app is plain HTML+JS — it talks to the host only via
 *  the injected `window.rela` SDK over the MessageChannel. */
const E2E_DEMO_APP_HTML = `<!doctype html>
<html lang="en">
  <head><meta charset="utf-8" /><title>E2E Demo</title>
    <meta name="rela-app:bridge-version" content="1">
    <meta name="rela-app:label" content="E2E Demo">
    <meta name="rela-app:description" content="Drives the rela bridge from a sandboxed iframe for e2e tests">
    <script src="_rela.js"></script>
    <!-- The optional markdown editor. A real <script src>, so the bundle runs
         under the same path-scoped CSP an app gets in production — which is the
         only place the editor's styling and ProseMirror's own DOM writes can
         actually be verified (TKT-D2JML7). -->
    <script src="_rela-editor.js"></script>
    <link rel="stylesheet" href="_rela.css" /></head>
  <body>
    <div data-testid="status">starting</div>
    <div data-testid="feature-count"></div>
    <button data-testid="link-btn" type="button">Link</button>
    <div data-testid="link-result"></div>
    <!-- CSP enforcement probe: the app's OWN JS tries to reach /api/ directly;
         the path-scoped CSP (connect-src 'none' + scoped img-src) must block it.
         Result lands in [data-testid=csp-probe]: 'blocked' if the boundary holds. -->
    <div data-testid="csp-probe">pending</div>
    <rela-editor data-testid="editor" placeholder="Write markdown"></rela-editor>
    <div data-testid="editor-value"></div>
    <div data-testid="editor-events"></div>
    <script src="app.js"></script>
  </body>
</html>`;

/** The e2e demo app's script, served as a sibling file.
 *
 *  A separate file, not an inline <script>: the app CSP carries no
 *  'unsafe-inline' (TKT-JO125X), so an inline block would be blocked and this
 *  app would do nothing — every apps.spec.ts assertion would fail on a blank
 *  page rather than on the behaviour under test. */
const E2E_DEMO_APP_JS = `var cspEl = document.querySelector('[data-testid=csp-probe]');
(function probeCSP() {
  // 1) connect-src 'none' must reject a direct fetch to the API.
  fetch('/api/v1/features/FEAT-001')
    .then(function () { cspEl.textContent = 'LEAK: fetch reached /api/'; })
    .catch(function () {
      // 2) img-src is path-scoped to the app, so an /api/ image must fail.
      var img = new Image();
      img.onload = function () { cspEl.textContent = 'LEAK: img loaded /api/'; };
      img.onerror = function () { cspEl.textContent = 'blocked'; };
      img.src = '/api/v1/features/FEAT-001';
    });
})();
function ready(fn) {
  var done = false;
  function go() { if (!done) { done = true; fn(); } }
  window.addEventListener('rela:ready', go, { once: true });
  setTimeout(go, 1000);
}
var statusEl = document.querySelector('[data-testid=status]');
var countEl = document.querySelector('[data-testid=feature-count]');
var linkResultEl = document.querySelector('[data-testid=link-result]');

ready(async function () {
  try {
    var res = await window.rela.list({ type: 'feature', params: { per_page: 200 } });
    var n = (res && res.data ? res.data.length : 0);
    countEl.textContent = String(n);
    statusEl.textContent = 'loaded';
  } catch (e) {
    statusEl.textContent = 'error: ' + (e && e.message ? e.message : e);
  }
});

// The <rela-editor> element's contract, mirrored into the DOM so the spec can
// read it: .value on one node, the input/change counts on another.
var editorEl = document.querySelector('[data-testid=editor]');
var editorValueEl = document.querySelector('[data-testid=editor-value]');
var editorEventsEl = document.querySelector('[data-testid=editor-events]');
var editorInputs = 0, editorChanges = 0;
function renderEditorState() {
  editorValueEl.textContent = JSON.stringify(editorEl.value);
  editorEventsEl.textContent = 'input=' + editorInputs + ' change=' + editorChanges;
}
editorEl.addEventListener('input', function () { editorInputs++; renderEditorState(); });
editorEl.addEventListener('change', function () { editorChanges++; renderEditorState(); });
ready(function () {
  // A body carrying the things a WYSIWYG round-trip is most likely to reformat:
  // a table, a list, and an entity reference.
  editorEl.value = '# Title\\n\\n- one\\n- two\\n\\nSee \`FEAT-001\` for detail.\\n\\n| a | b |\\n| - | - |\\n| 1 | 2 |\\n';
  renderEditorState();
});

document.querySelector('[data-testid=link-btn]').addEventListener('click', async function () {
  linkResultEl.textContent = 'linking';
  try {
    await window.rela.relationCreate({
      type: 'feature', id: 'FEAT-001', relation: 'blocks', targetId: 'FEAT-002',
    });
    linkResultEl.textContent = 'linked';
  } catch (e) {
    linkResultEl.textContent = 'error: ' + (e && e.message ? e.message : e);
  }
});`;

/** Document script rendered by rela-server when visiting
 *  /entity/feature/FEAT-001?doc=feature-overview. Emits a single link to
 *  a bug detail page — the rewriter will append ?return_to=<current url>
 *  so clicking the link lands on EntityView with a Back button. */
const SEED_DOCS: Record<string, string> = {
  "scripts/docs/feature_overview.lua": `-- Minimal doc for e2e round-trip testing.
local id = rela.document.entry_id
print("# " .. id)
print()
-- Link to a non-form internal route. The rewriter appends return_to on
-- these after TKT-JIEKC, so the landing EntityView renders a Back button.
print("[See bug BUG-001](" .. rela.url.detail({id = "BUG-001", type = "bug"}) .. ")")
`,
};

const SEED_ENTITIES: Record<string, string> = {
  "entities/features/FEAT-001.md": `---
id: FEAT-001
type: feature
title: User Authentication
status: approved
priority: high
---

Implement user authentication system.

- [ ] Define password policy
- [x] Pick OAuth provider
`,
  "entities/features/FEAT-002.md": `---
id: FEAT-002
type: feature
title: Dashboard Analytics
status: draft
priority: medium
---

Add analytics dashboard.
`,
  "entities/features/FEAT-003.md": `---
id: FEAT-003
type: feature
title: Export Data
status: in_progress
priority: low
---

Export data to CSV.
`,
  // FEAT-004 exists specifically to exercise GFM-checkbox rendering and
  // toggling; the body has one unchecked + one checked item so specs can
  // observe and flip either state.
  "entities/features/FEAT-004.md": `---
id: FEAT-004
type: feature
title: Checkbox render fixture
status: draft
priority: low
---

- [ ] First
- [x] Second
`,
  "entities/bugs/BUG-001.md": `---
id: BUG-001
type: bug
title: Login form validation
severity: high
status: draft
priority: high
---

Form validation is not working.
`,
  "entities/bugs/BUG-002.md": `---
id: BUG-002
type: bug
title: Memory leak in list view
severity: critical
status: in_progress
priority: high
---

Memory leak detected.
`,
  "entities/tasks/TASK-001.md": `---
id: TASK-001
type: task
title: Write unit tests
status: draft
assignee: Alice
done: false
---

Write unit tests for auth module.
`,
  // Unlinked task — reverse-relations.spec.ts picks this as a candidate when
  // adding a new incoming-direction implements edge to FEAT-001 from the
  // non-cards "Implemented by" picker on the feature edit form.
  "entities/tasks/TASK-002.md": `---
id: TASK-002
type: task
title: Refactor auth module
status: draft
---

Refactor the auth module to extract token validation.
`,
  // Seed a blocks relation with properties so relation-cards widget tests have
  // something rich to render. FEAT-001 blocks FEAT-003 with reason="test block",
  // severity=critical.
  "relations/FEAT-001--blocks--FEAT-003.md": `---
from: FEAT-001
relation: blocks
to: FEAT-003
reason: test block
severity: critical
impact_score: 8
is_workaround_available: false
---
`,
  // Seed a tagged relation (FEAT-001 tagged -> FEAT-002) so the tagged widget
  // has an entry to display.
  "relations/FEAT-001--tagged--FEAT-002.md": `---
from: FEAT-001
relation: tagged
to: FEAT-002
added_by: e2e-seed
added_date: "2026-01-15"
---
`,
  // TASK-001 implements FEAT-001 — used by reverse-relations.spec.ts to
  // verify that a non-cards relation widget with direction: incoming lists
  // the linked task(s) on the feature form.
  "relations/TASK-001--implements--FEAT-001.md": `---
from: TASK-001
relation: implements
to: FEAT-001
---
`,
};

/**
 * Feature-entity templates. The Vue form picks these up via GET
 * /api/v1/_templates/feature and renders one pill per template; clicking a
 * pill pre-fills the form. Two variants let the template-pill test assert
 * active-state switching. (The base `feature.md` is picked up as a default.)
 */
const SEED_TEMPLATES: Record<string, string> = {
  "templates/entities/feature.md": `---
status: draft
priority: medium
---

## Summary

Brief description of the feature.
`,
  "templates/entities/feature--spike.md": `---
status: draft
priority: low
---

## Spike goal

Timeboxed investigation. Document findings here.
`,
};
