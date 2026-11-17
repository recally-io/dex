export type ParsedSshTarget = {
  host: string;
  user?: string;
  port?: number;
  identityFile?: string;
  suggestedName?: string;
};

const sshValueFlags = new Set([
  "b",
  "B",
  "c",
  "D",
  "E",
  "e",
  "F",
  "I",
  "i",
  "J",
  "L",
  "l",
  "m",
  "O",
  "o",
  "p",
  "Q",
  "R",
  "S",
  "W",
  "w",
]);

function validPort(raw: string): number | undefined {
  if (!/^\d+$/.test(raw)) return undefined;
  const n = Number(raw);

  return Number.isInteger(n) && n >= 1 && n <= 65535 ? n : undefined;
}

function splitUserHostPort(raw: string): { host: string; user?: string; port?: number } | null {
  let rest = raw.trim();

  if (!rest || /\s/.test(rest)) return null;

  let user: string | undefined;
  const at = rest.lastIndexOf("@");

  if (at > 0) {
    const candidateUser = rest.slice(0, at);

    if (!candidateUser.includes(":") && !candidateUser.includes("/")) {
      user = candidateUser;
      rest = rest.slice(at + 1);
    }
  }

  let host = rest;
  let port: number | undefined;

  if (rest.startsWith("[")) {
    const close = rest.indexOf("]");

    if (close <= 1) return null;
    host = rest.slice(1, close);
    const tail = rest.slice(close + 1);

    if (tail.startsWith(":")) {
      port = validPort(tail.slice(1));

      if (port === undefined) return null;
    } else if (tail !== "") {
      return null;
    }

    return { host, user, port };
  }

  const firstColon = rest.indexOf(":");
  const lastColon = rest.lastIndexOf(":");

  if (firstColon !== -1 && firstColon === lastColon) {
    const candidatePort = validPort(rest.slice(lastColon + 1));

    if (candidatePort !== undefined) {
      host = rest.slice(0, lastColon);
      port = candidatePort;
    }
  }

  if (!host) return null;

  return { host, user, port };
}

function tokenizeShell(raw: string): string[] {
  const tokens: string[] = [];
  let cur = "";
  let quote: '"' | "'" | null = null;
  let started = false;

  for (let i = 0; i < raw.length; i++) {
    const ch = raw[i];

    if (quote) {
      if (ch === quote) quote = null;
      else {
        cur += ch;
        started = true;
      }

      continue;
    }

    if (ch === '"' || ch === "'") {
      quote = ch;
      started = true;
    } else if (/\s/.test(ch)) {
      if (started) {
        tokens.push(cur);
        cur = "";
        started = false;
      }
    } else {
      cur += ch;
      started = true;
    }
  }

  if (started) tokens.push(cur);

  return tokens;
}

function parseSshCommand(raw: string): ParsedSshTarget | null {
  const tokens = tokenizeShell(raw.trim());

  if (tokens.length < 2 || tokens[0].toLowerCase() !== "ssh") return null;

  let user: string | undefined;
  let port: number | undefined;
  let identityFile: string | undefined;
  let targetToken = "";

  for (let i = 1; i < tokens.length; i++) {
    const tok = tokens[i];

    if (tok === "--") {
      if (i + 1 < tokens.length) targetToken = tokens[i + 1];
      break;
    }

    if (tok.startsWith("-") && tok.length > 1) {
      const flag = tok[1];
      const attached = tok.slice(2);

      if (sshValueFlags.has(flag)) {
        const val = attached || tokens[++i] || "";

        if (flag === "p") port = validPort(val) ?? port;
        else if (flag === "i" && val) identityFile = val;
        else if (flag === "l" && val) user = val;
      }

      continue;
    }

    targetToken = tok;
    break;
  }

  if (!targetToken) return null;
  const parsedTarget = splitUserHostPort(targetToken);

  if (!parsedTarget) return null;

  return {
    host: parsedTarget.host,
    user: user || parsedTarget.user,
    port: port ?? parsedTarget.port,
    identityFile,
    suggestedName: parsedTarget.host,
  };
}

function parseSshConfigSnippet(raw: string): ParsedSshTarget | null {
  const lines = raw
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith("#"));

  if (lines.length < 2) return null;

  let alias = "";
  let hostName = "";
  let user: string | undefined;
  let port: number | undefined;
  let identityFile: string | undefined;
  let matchedDirective = false;

  for (const line of lines) {
    const eqOrSpace = line.match(/^([A-Za-z]+)(?:\s*=\s*|\s+)(.+)$/);

    if (!eqOrSpace) continue;
    const key = eqOrSpace[1].toLowerCase();
    const valTokens = tokenizeShell(eqOrSpace[2]);

    if (valTokens.length === 0) continue;

    switch (key) {
      case "host":
        if (!alias) {
          const firstConcrete = valTokens.find((p) => !p.startsWith("!") && !/[*?]/.test(p));

          if (firstConcrete) alias = firstConcrete;
        }

        break;
      case "hostname":
        hostName = valTokens[0];
        matchedDirective = true;
        break;
      case "user":
        user = valTokens[0];
        matchedDirective = true;
        break;
      case "port": {
        const parsed = validPort(valTokens[0]);

        if (parsed !== undefined) {
          port = parsed;
          matchedDirective = true;
        }

        break;
      }

      case "identityfile":
        identityFile = valTokens[0];
        matchedDirective = true;
        break;
    }
  }

  if (!matchedDirective && !alias) return null;
  const host = hostName || alias;

  if (!host) return null;

  return {
    host,
    user,
    port,
    identityFile,
    suggestedName: alias || host,
  };
}

export function parseSshTargetInput(raw: string): ParsedSshTarget | null {
  const trimmed = raw.trim();

  if (!trimmed) return null;

  if (/[\r\n]/.test(trimmed)) {
    return parseSshConfigSnippet(trimmed);
  }

  if (/^ssh\s+/i.test(trimmed)) {
    return parseSshCommand(trimmed);
  }

  if (/^ssh:\/\//i.test(trimmed)) {
    try {
      const url = new URL(trimmed);
      const host = url.hostname.replace(/^\[|\]$/g, "");

      if (!host) return null;
      const user = url.username ? decodeURIComponent(url.username) : undefined;
      const port = url.port ? validPort(url.port) : undefined;

      return { host, user, port, suggestedName: host };
    } catch {
      return null;
    }
  }

  const parsed = splitUserHostPort(trimmed);

  if (!parsed) return null;

  if (!parsed.user && parsed.port === undefined && !trimmed.startsWith("[")) {
    return null;
  }

  return { ...parsed, suggestedName: parsed.host };
}

export function inferE2BDomain(apiUrl: string): string {
  const trimmed = apiUrl.trim();

  if (!trimmed) return "";

  try {
    const url = new URL(trimmed);
    const lowerHost = url.hostname.toLowerCase();

    if (lowerHost.startsWith("api.") && lowerHost.length > 4) {
      return url.host.slice(4);
    }

    if (lowerHost === "localhost") {
      return url.host;
    }

    return "";
  } catch {
    return "";
  }
}
