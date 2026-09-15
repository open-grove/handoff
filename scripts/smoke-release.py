#!/usr/bin/env python3
"""Exercise built CLI publishing/reading against a running local handoffd.

Build with `make build`, start an isolated handoffd, then run:
  python3 scripts/smoke-release.py --server http://127.0.0.1:17391
Synthetic handoffs and HTML/JSON evidence remain in the local test directories.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import urlparse
from urllib.request import urlopen


BODY = """# 发布兼容性验证

## 项目背景

确认交接在当前对话整理后，可以直接发布并由接收方读取。

## 当前情况

已确认兼容问题的原因，正文和代码块应完整保留。

## 验证结果

| 场景 | 预期 |
| --- | --- |
| 当前对话 | 不启动第二个 AI |
| 继续工作 | 保留状态和下一步 |

```markdown
### Current State

代码示例中的标题不代表真正的状态。

## For Agent

请保留这里的空行。
```

## 下一步

- 修复兼容读取逻辑。
- 验证发布结果。

## 参考

[示例证据](https://example.com/evidence)

api_key=release-smoke-secret
"""


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", required=True)
    args = parser.parse_args()
    parsed = urlparse(args.server)
    if parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "::1"}:
        parser.error("use an HTTP loopback test server; this script creates synthetic handoffs")
    root = Path(__file__).resolve().parent.parent
    out = root / "bin/release-smoke"
    out.mkdir(parents=True, exist_ok=True)
    cli = root / "bin/handoff"
    env = dict(os.environ, HANDOFF_SERVER=args.server,
               HANDOFF_CONFIG=str(out / "config.json"),
               HANDOFF_OWNERSHIP_FILE=str(out / "ownership.json"),
               HANDOFF_NO_AUTO_UPDATE="1", VISUAL="true")
    source = out / "prepared.md"
    source.write_text(BODY)
    exact = BODY[BODY.index("```markdown"):BODY.index("\n```", BODY.index("```markdown")) + 4]
    results = []
    for intent in ["share", "continue"]:
        for review in [False, True]:
            key = intent + ("-review" if review else "")
            cmd = [str(cli), "create", "发布兼容性验证", "--intent", intent,
                   "--file", str(source), "--no-git", "--json"]
            cmd += ["--review"] if review else ["--attach-context"]
            result = json.loads(subprocess.check_output(cmd, env=env, text=True))
            artifact = result["handoff"]
            assert artifact["intent"] == intent and artifact["generator"] == "preserve", key
            assert "release-smoke-secret" not in artifact["markdown"], key
            assert exact in artifact["markdown"], f"{key}: code block changed during publish"
            fetched = subprocess.check_output([str(cli), "receive", result["share_url"]], env=env, text=True)
            assert exact in fetched, f"{key}: code block changed during receive"
            with urlopen(result["share_url"], timeout=10) as response:
                page = response.read().decode()
            assert "<table" in page and "<pre" in page, f"{key}: missing table or code rendering"
            assert "release-smoke-secret" not in page, f"{key}: unredacted secret on page"
            if not review:
                attachment = subprocess.check_output([str(cli), "context", result["share_url"]], env=env, text=True)
                assert "代码示例中的标题" in attachment and "release-smoke-secret" not in attachment, key
            (out / (key + ".json")).write_text(json.dumps(result, ensure_ascii=False, indent=2))
            (out / (key + ".html")).write_text(page)
            results.append({"case": key, "url": result["share_url"], "publish_receive_html": "passed"})
    (out / "results.json").write_text(json.dumps(results, ensure_ascii=False, indent=2))
    print(json.dumps(results, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
