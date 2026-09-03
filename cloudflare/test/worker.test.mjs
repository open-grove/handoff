import assert from "node:assert/strict";
import test from "node:test";

import worker, { renderHTML, renderHTMLLegacy, route } from "../src/index.mjs";

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
            records.set(values[0], { payload: values[1], delete_token_hash: values[3] });
          }
          if (sql.startsWith("INSERT INTO handoff_context_chunks")) {
            contextChunks.set(`${values[0]}:${values[1]}`, { handoff_id: values[0], chunk_index: values[1], payload: values[2] });
          }
          if (sql.startsWith("DELETE FROM handoffs") && sql.includes("WHERE id")) records.delete(values[0]);
          if (sql.startsWith("DELETE FROM handoff_context_chunks") && sql.includes("WHERE handoff_id = ?")) {
            for (const [key, row] of contextChunks) if (row.handoff_id === values[0]) contextChunks.delete(key);
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
      ttl_seconds: 1, // Legacy clients may still send this; it is ignored.
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
  assert.equal(created.handoff.expires_at, undefined);
  assert.doesNotMatch(created.handoff.markdown, /expires_at/);
  assert.ok(!JSON.stringify(db.records.get(created.handoff.id)).includes(created.delete_token));

  const apiResponse = await route(new Request(`https://handoff.example/v1/handoffs/${created.handoff.id}`), env, ctx);
  assert.equal(apiResponse.status, 200);
  assert.equal((await apiResponse.json()).delete_token, undefined);

  const pageResponse = await route(new Request(created.share_url), env, ctx);
  const page = await pageResponse.text();
  assert.equal(pageResponse.status, 200);
  assert.match(page, /<h1>继续交接工具<\/h1>/);
  assert.match(page, /核心流程可用/);
  assert.match(page, /class="panel human-panel"/);
  assert.match(page, /class="panel agent-panel"/);
  assert.match(page, /\.hero\{margin-bottom:22px;text-align:center\}/);
  assert.match(page, /class="brand-mark"><svg viewBox="0 0 128 128"/);
  assert.match(page, /fill="#5FB24A"/);
  assert.match(page, /class="brand" href="https:\/\/github.com\/open-grove\/handoff"/);
  for (const shellChange of [`class="response-card"`, `class="appendix"`, `class="title-block"`, `class="intent"`, `class="top-title"`]) assert.ok(!page.includes(shellChange));
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

test("legacy expiring records are normalized as permanent", async () => {
  const db = fakeDB();
  const id = "abcdefghijklmnopqrstuv";
  db.records.set(id, {
    payload: JSON.stringify({
      handoff: {
        version: 6,
        id,
        title: "Legacy",
        goal: "Keep this handoff",
        intent: "continue",
        source: { kind: "codex" },
        generator: "agent:codex",
        created_at: "2026-08-01T00:00:00Z",
        expires_at: "2026-08-08T00:00:00Z",
        markdown: "legacy markdown with expires_at",
      },
      sections: {
        intent: "continue",
        context: "Known",
        decisions: [],
        current_state: "Ready",
        important_files: [],
        next_steps: ["Continue"],
        open_questions: [],
      },
    }),
    delete_token_hash: "hash",
  });

  const response = await route(new Request(`https://handoff.example/v1/handoffs/${id}`), { HANDOFF_DB: db });
  assert.equal(response.status, 200);
  const fetched = await response.json();
  assert.equal(fetched.handoff.version, 7);
  assert.equal(fetched.handoff.expires_at, undefined);
  assert.doesNotMatch(fetched.handoff.markdown, /expires_at/);
});

test("share handoffs preserve conclusions without inventing task sections", async () => {
  const request = new Request("https://handoff.example/v1/handoffs", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      goal: "MCP App 架构讨论",
      source: { kind: "codex" },
      generator: "agent:codex",
      sections: {
        intent: "share",
        human_background: "",
        human_status: "这个状态应被清空。",
        human_todos: ["这个待办应被清空。"],
        human_sections: [
          { title: "先把三个东西分清楚", body: "MCP App 规定 App 与宿主如何通信，并不等于 App 本体。" },
          { title: "故事种子怎么通信", body: "隔离 App 无法直接调用宿主函数，所以使用 command.run 和 openLink。" },
          { title: "为什么不搬回宿主", body: "搬回宿主会重新引入发版耦合。" },
        ],
        context: "work-management view uses protocol: mcp-app.",
        decisions: ["保留 MCP App 通信通道。"],
        current_state: "这个状态应被清空。",
        important_files: ["cloudflare/src/index.mjs"],
        next_steps: ["这个下一步应被清空。"],
        open_questions: ["是否需要官方 App 信任分级？"],
      },
    }),
  });
  const response = await route(request, { HANDOFF_DB: fakeDB() });
  assert.equal(response.status, 201);
  const created = await response.json();
  assert.equal(created.handoff.version, 7);
  assert.equal(created.handoff.intent, "share");
  assert.match(created.handoff.markdown, /intent: "share"/);
  assert.match(created.handoff.markdown, /### 先把三个东西分清楚/);
  assert.match(created.handoff.markdown, /### 故事种子怎么通信/);
  assert.doesNotMatch(created.handoff.markdown, /### 关键结论|### 为什么会得出这些结论|### 帮助理解的例子/);
  assert.doesNotMatch(created.handoff.markdown, /### 待办事项|### Current State|### Next Steps/);
  assert.match(created.handoff.markdown, /> 这是一份讨论成果分享。/);
  const page = renderHTML(created.handoff, {
    intent: "share",
    human_sections: [{ title: "先把三个东西分清楚", body: "MCP App 是通信协议。\n\n> SDK 只是代码接口，不能单凭名字判断进程结构。\n\n- 理由和结论放在一起\n\n```text\nView -> Host\n```" }],
    context: "technical",
    decisions: ["保留"],
    important_files: [],
    open_questions: [],
  });
  assert.match(page, /讨论成果/);
  assert.match(page, /技术附录/);
  assert.match(page, /class="panel human-panel"/);
  assert.match(page, /class="panel-heading"/);
  assert.match(page, /🖐️/);
  assert.match(page, /🤖/);
  assert.match(page, /<ul><li>理由和结论放在一起<\/li><\/ul>/);
  assert.match(page, /<blockquote>SDK 只是代码接口，不能单凭名字判断进程结构。<\/blockquote>/);
  assert.match(page, /class="code-block"/);
  assert.match(page, /<span>plain text<\/span>/);
  assert.match(page, /<pre><code class="language-text">View -&gt; Host<\/code><\/pre>/);
  const legacyPage = renderHTMLLegacy(created.handoff, {
    intent: "share",
    human_sections: [{ title: "先把三个东西分清楚", body: "MCP App 是通信协议。" }],
    context: "technical",
    decisions: ["保留"],
    important_files: [],
    open_questions: [],
  });
  assert.match(legacyPage, /class="panel human-panel"/);
  assert.doesNotMatch(legacyPage, /class="response-card"/);
});

test("preserve handoffs keep long prepared Markdown without a failure banner or duplicate", async () => {
  const exactMarker = "PROMPT-END-" + "x".repeat(25_000);
  const body = `# 测试方法\n\n## 两轮 Prompt\n\nhttps://example.com/download.bin\n\n\`sha256:abc\`\n\n${exactMarker}`;
  const response = await route(new Request("https://handoff.example/v1/handoffs", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      goal: "测试方法",
      source: { kind: "stdin" },
      generator: "preserve",
      sections: {
        intent: "share",
        human_sections: [{ title: "ww-bedrock-single-turn-gray-test.md", body }],
        context: "正文由发送方准备，未调用旁路归纳 Agent。",
        decisions: [],
        important_files: [],
        open_questions: [],
      },
    }),
  }), { HANDOFF_DB: fakeDB() });
  assert.equal(response.status, 201);
  const created = await response.json();
  assert.equal(created.handoff.generator, "preserve");
  assert.match(created.handoff.markdown, new RegExp(exactMarker.slice(-80)));
  assert.equal((created.handoff.markdown.match(/https:\/\/example\.com\/download\.bin/g) || []).length, 1);
  assert.doesNotMatch(created.handoff.markdown, /ww-bedrock-single-turn-gray-test\.md|### 正文/);
  assert.equal((created.handoff.markdown.match(/# 测试方法/g) || []).length, 1);
  assert.doesNotMatch(created.handoff.markdown, /Agent 归纳不可用|未生成讨论摘要/);
  const page = renderHTML(created.handoff, {
    intent: "share",
    human_sections: [{ title: "正文", body: "## 两轮 Prompt\n\nExact" }],
    context: "未调用旁路归纳 Agent。",
    decisions: [], important_files: [], open_questions: [],
  });
  assert.doesNotMatch(page, /ww-bedrock-single-turn-gray-test\.md|<h3>正文<\/h3>|<h1>测试方法<\/h1>[^]*<h[1-4]>测试方法<\/h[1-4]>/);
});

test("share pages render inline and display LaTeX as native MathML", () => {
  const body = String.raw`Inline $s_A = f_\theta(p,r_A)$ works.

$$
P(A \succ B)=\frac{e^{s_A}}{e^{s_A}+e^{s_B}}
$$

\[
q=\frac{e^{s_{new}}}{e^{s_{new}}+e^{s_{reference}}}
\]` + "\n\n`$not_math$`";
  const page = renderHTML({
    id: "abcdefghijklmnopqrstuv",
    title: "Math",
    goal: "Explain math",
    intent: "share",
  }, {
    intent: "share",
    human_sections: [{ title: "公式", body }],
    context: String.raw`Agent detail: \(P(i \succ j)=\sigma(\beta_i-\beta_j)\).`,
    decisions: [],
    important_files: [],
    open_questions: [],
  });

  assert.match(page, /class="math-inline"/);
  assert.match(page, /class="math-display"/);
  assert.match(page, /<math[^>]*display="block"/);
  assert.match(page, /<mfrac>/);
  assert.match(page, /<msub>/);
  assert.match(page, /<msup>/);
  assert.match(page, /<code>\$not_math\$<\/code>/);
  assert.doesNotMatch(page, /<script/);
});

test("invalid or unsafe LaTeX falls back to escaped source", () => {
  const page = renderHTML({
    id: "abcdefghijklmnopqrstuv",
    title: "Safe math",
    goal: "Safe math",
    intent: "share",
  }, {
    intent: "share",
    human_sections: [{ title: "公式", body: String.raw`$\href{javascript:alert(1)}{x}$ and $\frac{1}{$` }],
    context: "",
    decisions: [],
    important_files: [],
    open_questions: [],
  });

  assert.match(page, /class="math-source"/);
  assert.doesNotMatch(page, /href="javascript:/);
  assert.doesNotMatch(page, /<script/);
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

test("removed cloud generation endpoints are not exposed", async () => {
  const env = { HANDOFF_DB: fakeDB() };
  for (const [method, path] of [
    ["GET", "/v1/schema/compact"],
    ["POST", "/v1/handoffs/compact"],
    ["POST", "/v1/handoffs/compact-preview"],
  ]) {
    const response = await route(new Request(`https://handoff.example${path}`, { method }), env);
    assert.equal(response.status, 404, `${method} ${path}`);
  }

  const health = await (await route(new Request("https://handoff.example/healthz"), env)).json();
  assert.equal("model_configured" in health, false);
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
