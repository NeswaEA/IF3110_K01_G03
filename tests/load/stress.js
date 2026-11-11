// Stress test: reads plus migration jobs, ramped from the normal load to 8x
// and back, to see where the API and the queue saturate and how they recover.
//
//   PROFILE=stress (default) | smoke     smoke runs 1 minute at low rate
//   TARGETS=spread (default) | same      jobs on load_1..load_4, or all on load_1
//   READ_RPS=20 JOB_RPS=1                the normal load
//
// A 202 from execute is not success: a job counts only when its execution
// reaches a final status.
import { check } from "k6";
import exec from "k6/execution";
import { Counter, Trend } from "k6/metrics";
import { API, get, login, post, waitForExecution } from "./lib.js";

const LOAD_DBS = ["30000000-0000-4000-8000-000000000011", "30000000-0000-4000-8000-000000000012",
  "30000000-0000-4000-8000-000000000013", "30000000-0000-4000-8000-000000000014"];
const smoke = __ENV.PROFILE === "smoke";
const targets = __ENV.TARGETS === "same" ? LOAD_DBS.slice(0, 1) : LOAD_DBS;
const readRps = Number(__ENV.READ_RPS || 20);
const jobRps = Number(__ENV.JOB_RPS || 1);
const jobsPerMinute = smoke ? 30 : Math.round(jobRps * 60);
const editor = { user: __ENV.EDITOR_USER || "editor", password: __ENV.EDITOR_PASSWORD };
const leader = { user: __ENV.LEADER_USER || "lead", password: __ENV.LEADER_PASSWORD || __ENV.EDITOR_PASSWORD };

// normal for 2+2 min, then 2x, 4x, 8x for 2 min each, back to normal and hold: 14 min
const stages = (n) =>
  smoke
    ? [{ target: n, duration: "20s" }, { target: n, duration: "40s" }]
    : [
        { target: n, duration: "2m" }, { target: n, duration: "2m" },
        { target: 2 * n, duration: "2m" }, { target: 4 * n, duration: "2m" }, { target: 8 * n, duration: "2m" },
        { target: n, duration: "2m" }, { target: n, duration: "2m" },
      ];
// arrivals over the whole run for a rate of n per second (or per minute, /60)
const area = (n) => stages(n).reduce((acc, s, i, all) => {
  const from = i === 0 ? 0 : all[i - 1].target;
  return acc + ((from + s.target) / 2) * (parseInt(s.duration) * (s.duration.endsWith("m") ? 60 : 1));
}, 0);

export const options = {
  setupTimeout: "15m",
  scenarios: {
    api_read: {
      executor: "ramping-arrival-rate", exec: "apiRead", timeUnit: "1s", startRate: 0,
      preAllocatedVUs: 20, maxVUs: 200, stages: stages(smoke ? 2 : readRps),
    },
    job_submit: {
      // per minute, so the normal rate and the smoke rate stay whole numbers
      executor: "ramping-arrival-rate", exec: "jobSubmit", timeUnit: "1m", startRate: 0,
      preAllocatedVUs: 20, maxVUs: 400, stages: stages(jobsPerMinute),
    },
  },
  thresholds: {
    "http_req_duration{scenario:api_read}": ["p(95)<300"],
    "http_req_duration{name:execute}": ["p(95)<500"],
    http_req_failed: ["rate<0.01"],
    checks: ["rate>0.99"],
  },
};

const queueWait = new Trend("queue_wait_ms", true);
const jobDuration = new Trend("job_duration_ms", true);
const jobE2E = new Trend("job_e2e_ms", true);
const jobsCompleted = new Counter("jobs_completed");
const jobsFailed = new Counter("jobs_failed");

// setup prepares every job up front: an approved apply request per migration,
// so the measured scenario only executes and waits.
export function setup() {
  if (!editor.password) throw new Error("set EDITOR_PASSWORD (and LEADER_PASSWORD if it differs)");
  const jobs = Math.ceil((area(jobsPerMinute) / 60) * 1.1); // arrivals over the run, plus slack
  const author = login(editor.user, editor.password);
  const requests = [];
  for (let i = 0; i < jobs; i++) {
    const db = targets[i % targets.length];
    const lo = (i % 100) * 10 + 1;
    const created = post(author, `/databases/${db}/migrations`, {
      title: `k6 load ${i + 1}`,
      up_sql: `UPDATE public.load_items SET qty = qty + 1, updated_at = now() WHERE id BETWEEN ${lo} AND ${lo + 9}`,
      down_sql: `UPDATE public.load_items SET qty = qty - 1, updated_at = now() WHERE id BETWEEN ${lo} AND ${lo + 9}`,
      mode: "normal",
    }, "setup_migration");
    if (created.status !== 201) throw new Error(`create migration: HTTP ${created.status} ${created.body}`);
    const req = post(author, `/databases/${db}/migrations/${created.json("data.id")}/requests`, { action: "apply" }, "setup_request");
    if (req.status !== 201) throw new Error(`request: HTTP ${req.status} ${req.body}`);
    requests.push({ db, id: req.json("data.id") });
  }
  const approver = login(leader.user, leader.password);
  for (const r of requests) {
    const res = post(approver, `/databases/${r.db}/change-requests/${r.id}/approve`, {}, "setup_approve");
    if (res.status !== 200 || res.json("data.status") !== "approved") throw new Error(`approve: HTTP ${res.status} ${res.body}`);
  }
  return { requests };
}

let session; // one login per VU, kept across iterations

export function apiRead() {
  const db = targets[exec.scenario.iterationInTest % targets.length];
  const reads = [
    () => get("/databases", "databases"),
    () => get(`/databases/${db}/schema`, "schema"),
    () => get(`/databases/${db}/data?schema=public&table=load_items&limit=50`, "data"),
    () => get(`/databases/${db}/history?limit=20`, "history"),
  ];
  session ??= login(editor.user, editor.password);
  const res = reads[exec.scenario.iterationInTest % reads.length]();
  check(res, { "read 200": (r) => r.status === 200 });
}

export function jobSubmit(data) {
  const r = data.requests[exec.scenario.iterationInTest];
  if (!r) return; // more arrivals than prepared jobs
  session ??= login(editor.user, editor.password);
  const started = Date.now();
  const res = post(session, `/databases/${r.db}/change-requests/${r.id}/execute`, {}, "execute");
  if (!check(res, { "execute 202": (x) => x.status === 202 })) return;
  const e = waitForExecution(r.db, res.json("data.id"), 600);
  jobE2E.add(Date.now() - started);
  const ok = check(e, { "job succeeded": (x) => x !== null && x.status === "succeeded" });
  (ok ? jobsCompleted : jobsFailed).add(1);
  if (e && e.started_at) {
    queueWait.add(Date.parse(e.started_at) - Date.parse(e.enqueued_at));
    if (e.finished_at) jobDuration.add(Date.parse(e.finished_at) - Date.parse(e.started_at));
  }
}

export function handleSummary(data) {
  return { stdout: JSON.stringify({ api: API, targets: targets.length, metrics: data.metrics }, null, 1) };
}
