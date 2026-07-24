import assert from "node:assert/strict";
import test from "node:test";

import worker, { readAgentPlanText, renderHTML, route } from "../src/index.mjs";

function fakeDB() {
  const records = new Map();
  const contextChunks = new Map();
  return {
    records,
    contextChunks,
    async batch(statements) {
      for (const statement of statements) await statement.run();
      return statements.map(() => ({ success: true }));
    },
    prepare(sql) {
      let values = [];
      return {
        bind(...bound) {
          values = bound;
          return this;
        },
        async run() {
          if (sql.startsWith("INSERT INTO handoffs")) {
            records.set(values[0], { payload: values[1], expires_at: values[2], delete_token_hash: values[3] });
          }
          if (sql.startsWith("INSERT INTO handoff_context_chunks")) {
            contextChunks.set(`${values[0]}:${values[1]}`, { handoff_id: values[0], chunk_index: values[1], payload: values[2] });
          }
          if (sql.startsWith("DELETE FROM handoffs") && sql.includes("WHERE id")) records.delete(values[0]);
          if (sql.startsWith("DELETE FROM handoff_context_chunks") && sql.includes("WHERE handoff_id = ?")) {
            for (const [key, row] of contextChunks) if (row.handoff_id === values[0]) contextChunks.delete(key);
          }
          if (sql.startsWith("DELETE") && sql.includes("expires_at")) {
            for (const [id, row] of records) if (row.expires_at <= values[0]) records.delete(id);
          }
          return { success: true };
        },
        async first() {
          return records.get(values[0]) || null;
        },
        async all() {
          if (sql.startsWith("SELECT payload FROM handoff_context_chunks")) {
            return {
              results: [...contextChunks.values()]
                .filter((row) => row.handoff_id === values[0])
                .sort((left, right) => left.chunk_index - right.chunk_index)
                .map((row) => ({ payload: row.payload })),
            };
          }
          return { results: [] };
        },
      };
    },
  };
}

function publishRequest() {
  return new Request("https://handoff.example/v1/handoffs", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
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
  assert.match(created.delete_token, /^[A-Za-z0-9_-]{43}$/);
  assert.ok(!JSON.stringify(db.records.get(created.handoff.id)).includes(created.delete_token));

  const apiResponse = await route(new Request(`https://handoff.example/v1/handoffs/${created.handoff.id}`), env, ctx);
  assert.equal(apiResponse.status, 200);
  assert.equal((await apiResponse.json()).delete_token, undefined);

  const pageResponse = await route(new Request(created.share_url), env, ctx);
  const page = await pageResponse.text();
  assert.equal(pageResponse.status, 200);
  assert.match(page, /<h1>继续交接工具<\/h1>/);
  assert.match(page, /核心流程可用/);
  assert.match(page, /\.human-content\{display:grid;grid-template-columns:1fr;gap:0\}/);
  assert.match(page, /\.hero\{margin-bottom:22px;text-align:center\}/);
  assert.match(page, /class="brand-mark"><svg viewBox="0 0 128 128"/);
  assert.match(page, /fill="#5FB24A"/);
  assert.match(page, /class="brand" href="https:\/\/github.com\/open-grove\/handoff"/);
  for (const removed of ["READY TO CONTINUE", "有效期至", "Shared with OpenGrove", `class="brand-mark">OG`]) assert.ok(!page.includes(removed));

  const markdownResponse = await route(new Request(`${created.share_url}.md`), env, ctx);
  const markdown = await markdownResponse.text();
  assert.match(markdown, /## For Agent/);
  assert.match(markdown, /> 这是一份被传递的 Handoff。请先用清晰易懂的话向用户简单介绍当前背景，然后询问用户下一步要怎么做。\s*$/);

  const rejectedDelete = await route(new Request(`https://handoff.example/v1/handoffs/${created.handoff.id}`, {
    method: "DELETE",
    headers: { "X-Handoff-Delete-Token": "wrong-token" },
  }), env, ctx);
  assert.equal(rejectedDelete.status, 401);

  const deleted = await route(new Request(`https://handoff.example/v1/handoffs/${created.handoff.id}`, {
    method: "DELETE",
    headers: { "X-Handoff-Delete-Token": created.delete_token },
  }), env, ctx);
  assert.equal(deleted.status, 204);
  assert.equal(db.records.size, 0);
});

test("long goals get a compact display title without losing the Agent goal", async () => {
  const request = publishRequest();
  const body = await request.json();
  body.goal = "继续完成编辑部 0.1.12 发布：核实并安全修复团队权限，测试后重新发布";
  const response = await route(new Request(request.url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  }), { HANDOFF_DB: fakeDB() });
  assert.equal(response.status, 201);
  const created = await response.json();
  assert.equal(created.handoff.title, "继续完成编辑部 0.1.12 发布");
  assert.match(created.handoff.markdown, /^# 继续完成编辑部 0\.1\.12 发布$/m);
  assert.match(created.handoff.markdown, /继续完成编辑部 0\.1\.12 发布：核实并安全修复团队权限，测试后重新发布/);
  const page = renderHTML(created.handoff, body.sections);
  assert.match(page, /<h1>继续完成编辑部 0\.1\.12 发布<\/h1>/);
  assert.doesNotMatch(page, /<h1>[^<]*核实并安全修复/);
});

test("publishing is anonymous while server compaction requires OpenGrove login", async () => {
  const env = {
    HANDOFF_DB: fakeDB(),
    HANDOFF_API_TOKEN: "test-token",
    OPENGROVE_AUTH_FETCH: async (_url, init) => {
      const token = String(init?.headers?.Authorization || "");
      if (token !== "Bearer valid-opengrove-token") {
        return new Response(JSON.stringify({ error: "unauthorized" }), { status: 401 });
      }
      return Response.json({ data: { user_id: "user-1" } });
    },
  };
  assert.equal((await route(publishRequest(), env)).status, 201);

  const compactBody = JSON.stringify({
    goal: "continue",
    context: { source: "stdin", messages: [{ role: "user", text: "known context" }] },
  });
  const unauthenticated = await route(new Request("https://handoff.example/v1/handoffs/compact-preview", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: compactBody,
  }), env);
  assert.equal(unauthenticated.status, 401);

  const authenticated = await route(new Request("https://handoff.example/v1/handoffs/compact-preview", {
    method: "POST",
    headers: {
      Authorization: "Bearer valid-opengrove-token",
      "Content-Type": "application/json",
    },
    body: compactBody,
  }), env);
  assert.equal(authenticated.status, 200);
});

test("cloud generation receives complete canonical readable context while redacting private identifiers", async () => {
  let prompt = "";
  const sections = JSON.stringify({
    human_background: "A",
    human_status: "B",
    human_todos: ["C"],
    context: "D",
    decisions: [],
    current_state: "E",
    important_files: [],
    next_steps: ["F"],
    open_questions: [],
  });
  const env = {
    HANDOFF_DB: fakeDB(),
    ARK_AGENT_PLAN_API_KEY: "secret",
    OPENGROVE_AUTH_FETCH: async () => Response.json({ data: { user_id: "user-1" } }),
    ARK_AGENT_PLAN_FETCH: async (_url, init) => {
      const body = JSON.parse(init.body);
      prompt = body.messages[0].content;
      return Response.json({ content: [{ type: "text", text: sections }], usage: { input_tokens: 1, output_tokens: 1 } });
    },
  };
  const beginning = `BEGIN alice@example.com 203.0.113.9 ${"a".repeat(180_000)}`;
  const response = await route(new Request("https://handoff.example/v1/handoffs/compact-preview", {
    method: "POST",
    headers: {
      Authorization: "Bearer valid-opengrove-token",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      goal: "continue",
      context: {
        source: "codex",
        summary: "auxiliary native compact summary",
        messages: [
          { role: "user", text: beginning },
          { role: "assistant", text: "END" },
        ],
      },
    }),
  }), env);
  assert.equal(response.status, 200);
  assert.match(prompt, /BEGIN/);
  assert.match(prompt, /END/);
  assert.match(prompt, /auxiliary native compact summary/);
  assert.doesNotMatch(prompt, /alice@example\.com|203\.0\.113\.9/);
  assert.match(prompt, /\[REDACTED EMAIL\].*\[REDACTED IP\]/);
});

test("an attached Context is persisted explicitly and fetched separately", async () => {
  const db = fakeDB();
  const env = { HANDOFF_DB: db };
  const request = publishRequest();
  const body = await request.json();
  body.context_attachment = {
    version: 999,
    source: {
      kind: "codex",
      session_id: "must-not-persist",
      cursor: "line:100",
      updated_at: "2026-07-24T00:00:00Z",
    },
    native_summary: "summary with alice@example.com",
    messages: [
      { role: "user", text: `private detail at /Users/alice/work/demo ${"x".repeat(600_000)}` },
      { role: "tool", text: "tool result must not persist" },
      { role: "assistant", text: "done" },
    ],
    repository: {
      root: "/Users/alice/work/demo",
      branch: "main",
      changed_files: ["README.md"],
    },
  };
  const createdResponse = await route(new Request(request.url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  }), env);
  assert.equal(createdResponse.status, 201);
  const created = await createdResponse.json();
  assert.equal(created.handoff.context.available, true);
  assert.equal(created.handoff.context.message_count, 2);
  assert.match(created.handoff.markdown, /### Attached Context/);
  assert.match(created.handoff.markdown, /handoff context opengrove-handoff:/);

  const regular = await route(new Request(`https://handoff.example/v1/handoffs/${created.handoff.id}`), env);
  const regularText = await regular.text();
  assert.doesNotMatch(regularText, /private detail|must-not-persist|line:100/);

  const attached = await route(new Request(`https://handoff.example/v1/handoffs/${created.handoff.id}/context`), env);
  assert.equal(attached.status, 200);
  const context = await attached.json();
  assert.equal(context.handoff_id, created.handoff.id);
  assert.equal(context.context.version, 1);
  assert.equal(context.context.redaction, "best-effort-v1");
  assert.equal(context.context.messages.length, 2);
  assert.match(context.context.messages[0].text, /\$HOME\/work\/demo/);
  assert.doesNotMatch(JSON.stringify(context), /must-not-persist|line:100|alice@example\.com|tool result/);
  assert.ok(db.contextChunks.size >= 3);

  const deleted = await route(new Request(`https://handoff.example/v1/handoffs/${created.handoff.id}`, {
    method: "DELETE",
    headers: { "X-Handoff-Delete-Token": created.delete_token },
  }), env);
  assert.equal(deleted.status, 204);
  assert.equal(db.contextChunks.size, 0);
});

test("anonymous publishing is rate limited when the Cloudflare binding rejects it", async () => {
  const response = await route(publishRequest(), {
    HANDOFF_DB: fakeDB(),
    HANDOFF_CREATE_RATE_LIMITER: { async limit() { return { success: false }; } },
  });
  assert.equal(response.status, 429);
});

test("Important Files keep repository-relative paths only", async () => {
  const request = publishRequest();
  const body = await request.json();
  body.sections.important_files = [
    "$WORKSPACE/cloudflare/src/index.mjs",
    "$HOME/Downloads/private.txt",
    "/Users/alice/work/demo/README.md",
    "internal/card/card.go",
    "../outside.txt",
  ];
  const response = await route(new Request(request.url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  }), { HANDOFF_DB: fakeDB() });
  assert.equal(response.status, 201);
  const created = await response.json();
  assert.deepEqual(
    created.handoff.markdown.match(/### Important Files\n\n([\s\S]*?)\n\n### Next Steps/)?.[1].split("\n"),
    ["- cloudflare/src/index.mjs", "- internal/card/card.go"],
  );
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

test("Agent Plan SSE responses are assembled into one result", async () => {
  const response = new Response([
    'event: content_block_start\ndata: {"type":"content_block_start","content_block":{"type":"text","text":""}}',
    'event: content_block_delta\ndata: {"type":"content_block_delta","delta":{"type":"text_delta","text":"{\\"human_background\\":\\"A\\","}}',
    'event: content_block_delta\ndata: {"type":"content_block_delta","delta":{"type":"text_delta","text":"\\"human_status\\":\\"B\\"}"}}',
    'event: message_stop\ndata: {"type":"message_stop"}',
    'data: [DONE]',
  ].join("\n\n"), { headers: { "Content-Type": "text/event-stream" } });
  assert.equal(await readAgentPlanText(response), '{"human_background":"A","human_status":"B"}');
});

test("compact preview streams before Agent Plan finishes and returns diagnostics", async () => {
  const completeSections = JSON.stringify({
    human_background: "A",
    human_status: "B",
    human_todos: ["C"],
    context: "D",
    decisions: [],
    current_state: "E",
    important_files: [],
    next_steps: ["F"],
    open_questions: [],
  });
  let release;
  const released = new Promise((resolve) => { release = resolve; });
  const upstream = new ReadableStream({
    async start(controller) {
      const encoder = new TextEncoder();
      controller.enqueue(encoder.encode(
        `event: content_block_delta\ndata: ${JSON.stringify({ type: "content_block_delta", delta: { type: "text_delta", text: completeSections.slice(0, 20) } })}\n\n`,
      ));
      await released;
      controller.enqueue(encoder.encode(
        `event: content_block_delta\ndata: ${JSON.stringify({ type: "content_block_delta", delta: { type: "text_delta", text: completeSections.slice(20) } })}\n\n`,
      ));
      controller.enqueue(encoder.encode(
        'event: message_delta\ndata: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":123}}\n\n',
      ));
      controller.close();
    },
  });
  const env = {
    HANDOFF_DB: fakeDB(),
    ARK_AGENT_PLAN_API_KEY: "secret",
    OPENGROVE_AUTH_FETCH: async () => Response.json({ data: { user_id: "user-1" } }),
    ARK_AGENT_PLAN_FETCH: async () => new Response(upstream, {
      status: 200,
      headers: { "Content-Type": "text/event-stream", "x-request-id": "upstream-1" },
    }),
  };
  const compactBody = JSON.stringify({
    goal: "continue",
    context: { source: "stdin", messages: [{ role: "user", text: "known context" }] },
  });
  const streamed = await route(new Request("https://handoff.example/v1/handoffs/compact-preview", {
    method: "POST",
    headers: {
      Accept: "text/event-stream",
      Authorization: "Bearer valid-opengrove-token",
      "Content-Type": "application/json",
    },
    body: compactBody,
  }), env);
  assert.match(streamed.headers.get("Content-Type"), /text\/event-stream/);
  const reader = streamed.body.getReader();
  const first = new TextDecoder().decode((await reader.read()).value);
  assert.match(first, /event: start/);
  release();

  let rest = "";
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    rest += new TextDecoder().decode(value);
  }
  assert.match(rest, /event: delta/);
  assert.match(rest, /event: result/);
  assert.match(rest, /"generator":"server:agent-plan"/);
  assert.match(rest, /"output_tokens":123/);
  assert.match(rest, /"upstream_request_id":"upstream-1"/);
});
