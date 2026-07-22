import assert from "node:assert/strict";
import test from "node:test";

import worker, { renderHTML, route } from "../src/index.mjs";

function fakeDB() {
  const records = new Map();
  return {
    records,
    prepare(sql) {
      let values = [];
      return {
        bind(...bound) {
          values = bound;
          return this;
        },
        async run() {
          if (sql.startsWith("INSERT")) records.set(values[0], { payload: values[1], expires_at: values[2] });
          if (sql.startsWith("DELETE") && sql.includes("WHERE id")) records.delete(values[0]);
          if (sql.startsWith("DELETE") && sql.includes("expires_at")) {
            for (const [id, row] of records) if (row.expires_at <= values[0]) records.delete(id);
          }
          return { success: true };
        },
        async first() {
          return records.get(values[0]) || null;
        },
      };
    },
  };
}

function publishRequest(token = "test-token") {
  return new Request("https://handoff.example/v1/handoffs", {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({
      goal: "继续交接工具",
      source: { kind: "codex" },
      generator: "agent:codex",
      sections: {
        human_background: "让同事接住已有工作。",
        human_status: "核心流程可用。",
        human_todos: ["部署"],
        context: "CLI and service are implemented.",
        decisions: [],
        current_state: "Tests pass.",
        important_files: ["README.md"],
        next_steps: ["Deploy"],
        open_questions: [],
      },
    }),
  });
}

test("publish, read, render, and delete a handoff", async () => {
  const db = fakeDB();
  const env = { HANDOFF_DB: db, HANDOFF_API_TOKEN: "test-token" };
  const ctx = { waitUntil() {} };

  const createdResponse = await route(publishRequest(), env, ctx);
  assert.equal(createdResponse.status, 201);
  const created = await createdResponse.json();
  assert.match(created.handoff.id, /^[A-Za-z0-9_-]{22}$/);
  assert.equal(created.share_url, `https://handoff.example/h/${created.handoff.id}`);

  const apiResponse = await route(new Request(`https://handoff.example/v1/handoffs/${created.handoff.id}`), env, ctx);
  assert.equal(apiResponse.status, 200);

  const pageResponse = await route(new Request(created.share_url), env, ctx);
  const page = await pageResponse.text();
  assert.equal(pageResponse.status, 200);
  assert.match(page, /<h1>继续交接工具<\/h1>/);
  assert.match(page, /核心流程可用/);
  assert.match(page, /\.human-content\{display:grid;grid-template-columns:1fr;gap:0\}/);
  for (const removed of ["READY TO CONTINUE", "有效期至", "Shared with OpenGrove"]) assert.ok(!page.includes(removed));

  const markdownResponse = await route(new Request(`${created.share_url}.md`), env, ctx);
  assert.match(await markdownResponse.text(), /## For Agent/);

  const deleted = await route(new Request(`https://handoff.example/v1/handoffs/${created.handoff.id}`, {
    method: "DELETE",
    headers: { Authorization: "Bearer test-token" },
  }), env, ctx);
  assert.equal(deleted.status, 204);
  assert.equal(db.records.size, 0);
});

test("write endpoints require authentication", async () => {
  const response = await route(publishRequest("wrong-token"), {
    HANDOFF_DB: fakeDB(),
    HANDOFF_API_TOKEN: "test-token",
  });
  assert.equal(response.status, 401);
});

test("HTML escapes untrusted content", () => {
  const html = renderHTML({ id: "abcdefghijklmnopqrstuv", goal: "<script>alert(1)</script>" }, {
    human_background: "<img src=x>", human_status: "Ready", human_todos: [], context: "Known",
    decisions: [], current_state: "Ready", important_files: [], next_steps: ["Continue"], open_questions: [],
  });
  assert.ok(!html.includes("<script>alert(1)</script>"));
  assert.ok(!html.includes("<img src=x>"));
});

test("top-level worker converts unexpected failures to safe JSON", async () => {
  const result = await worker.fetch(new Request("https://handoff.example/v1/handoffs"), {}, { waitUntil() {} });
  assert.equal(result.status, 404);
});
