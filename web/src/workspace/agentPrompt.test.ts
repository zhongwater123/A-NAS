import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import { connectionCheck, defaultAgentName, promptFor } from "./agentPrompt";

// The Go chain test sends the same request through the product service
// (internal/workspaceservice/chain_test.go).
const sample = JSON.parse(readFileSync("../internal/workspacemcp/testdata/connection-check.json", "utf8")) as {
  body: string;
  headers: Record<string, string>;
  answerHas: string;
};

const endpoint = "http://192.168.1.10/api/v1/workspace/mcp";
const token = `anas_ws_${"a".repeat(52)}`;

describe("the prompt that connects an agent", () => {
  it("checks a Codex's connection with the request the service answers", () => {
    expect(connectionCheck).toEqual({
      body: sample.body,
      contentType: sample.headers["Content-Type"],
      accept: sample.headers["Accept"],
      answerHas: sample.answerHas,
    });
    const prompt = promptFor("codex", endpoint, token);
    const curl = prompt.split("\n").find((line) => line.startsWith("curl "));
    expect(curl).toBe(
      `curl -sS -X POST "${endpoint}" -H "Authorization: Bearer ${token}" ` +
        `-H "Content-Type: ${sample.headers["Content-Type"]}" -H "Accept: ${sample.headers["Accept"]}" -d '${sample.body}'`,
    );
    const powershell = prompt.split("\n").find((line) => line.startsWith("(Invoke-RestMethod "));
    expect(powershell).toBe(
      `(Invoke-RestMethod -Method Post -Uri "${endpoint}" -Headers @{ Authorization = "Bearer ${token}"; ` +
        `Accept = "${sample.headers["Accept"]}" } -ContentType "${sample.headers["Content-Type"]}" ` +
        `-Body '${sample.body}').result.tools.name`,
    );
  });

  it("has a Codex keep its token in its own configuration, the same on every system", () => {
    const prompt = promptFor("codex", endpoint, token);
    expect(prompt).toContain(
      `[mcp_servers.a-nas]\nurl = "${endpoint}"\nhttp_headers = { Authorization = "Bearer ${token}" }`,
    );
    expect(prompt).toContain("~/.codex/config.toml");
    expect(prompt).toContain("%USERPROFILE%\\.codex\\config.toml");
    expect(prompt).not.toMatch(/SetEnvironmentVariable|bearer-token-env-var/);
  });

  it("gives Claude Code the command that works on every system, after taking out an earlier one", () => {
    const lines = promptFor("claude-code", endpoint, token).split("\n");
    const remove = lines.indexOf("claude mcp remove a-nas --scope user");
    const add = lines.indexOf(
      `claude mcp add --transport http --scope user a-nas ${endpoint} --header "Authorization: Bearer ${token}"`,
    );
    expect(remove).toBeGreaterThan(-1);
    expect(add).toBeGreaterThan(remove);
    expect(lines.some((line) => line.includes("claude mcp get a-nas"))).toBe(true);
  });

  it("says what the workspace is and what to tell the member, since members send it as it is", () => {
    for (const kind of ["claude-code", "codex"] as const) {
      const prompt = promptFor(kind, endpoint, token);
      expect(prompt).toMatch(/^请把我们团队 NAS 上的“A-NAS 共享工作区”接入为你的 MCP 服务器/);
      expect(prompt).toContain("不要在回复里重复令牌");
      expect(prompt).toContain("“把这个改小”");
    }
  });

  it("names the agent after the computer the browser runs on", () => {
    expect(defaultAgentName("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")).toBe("Mac 电脑");
    expect(defaultAgentName("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")).toBe("Windows 电脑");
    expect(defaultAgentName("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36")).toBe("Linux 电脑");
    expect(defaultAgentName("Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36")).toBe("");
  });
});
