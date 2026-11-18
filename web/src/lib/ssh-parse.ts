type ParsedSshTarget = {
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

function parseSshCommand(raw: string): ParsedSshTarget | null {
  const tokens = raw.trim().match(/(?:"[^"]*"|'[^']*'|\S)+/g) ?? [];
  const unquoted = tokens.map((tok) => tok.replace(/^["']|["']$/g, ""));

  if (unquoted.length < 2 || unquoted[0].toLowerCase() !== "ssh") return null;

  let user: string | undefined;
  let port: number | undefined;
  let identityFile: string | undefined;
  let targetToken = "";

  for (let i = 1; i < unquoted.length; i++) {
    const tok = unquoted[i];

    if (tok === "--") {
      targetToken = unquoted[i + 1] ?? "";
      break;
    }

    if (tok.startsWith("-") && tok.length > 1) {
      const flag = tok[1];
      const attached = tok.slice(2);

      if (sshValueFlags.has(flag)) {
        const val = attached || unquoted[++i] || "";

        if (flag === "p") port = validPort(val) ?? port;
        else if (flag === "i" && val) identityFile = val;
        else if (flag === "l" && val) user = val;
      }

      continue;
    }

    targetToken = tok;
    break;
  }

  const parsedTarget = targetToken ? splitUserHostPort(targetToken) : null;

  if (!parsedTarget) return null;

  return {
    host: parsedTarget.host,
    user: user || parsedTarget.user,
    port: port ?? parsedTarget.port,
    identityFile,
    suggestedName: parsedTarget.host,
  };
}

export function parseSshTargetInput(raw: string): ParsedSshTarget | null {
  const trimmed = raw.trim();

  if (!trimmed || /[\r\n]/.test(trimmed)) return null;

  if (/^ssh\s+/i.test(trimmed)) return parseSshCommand(trimmed);

  if (/^ssh:\/\//i.test(trimmed)) {
    try {
      const url = new URL(trimmed);
      const host = url.hostname.replace(/^\[|\]$/g, "");

      if (!host) return null;

      return {
        host,
        user: url.username ? decodeURIComponent(url.username) : undefined,
        port: url.port ? validPort(url.port) : undefined,
        suggestedName: host,
      };
    } catch {
      return null;
    }
  }

  const parsed = splitUserHostPort(trimmed);

  if (!parsed || (!parsed.user && parsed.port === undefined && !trimmed.startsWith("["))) {
    return null;
  }

  return { ...parsed, suggestedName: parsed.host };
}
