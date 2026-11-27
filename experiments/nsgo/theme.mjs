// The Go module cannot embed a file outside its directory. Export only the Web
// tokens that the native controls use. The app embeds the result on all targets.
import { readFileSync, writeFileSync } from "node:fs";

const css = readFileSync(new URL("../../web/src/styles.css", import.meta.url), "utf8");
const roles = {
  background: ["background", "field"],
  foreground: ["text", "code"],
  card: ["surface", "raised", "knob"],
  primary: ["accent", "accentFill", "light"],
  "primary-foreground": ["accentText", "onLight"],
  muted: ["inset", "surfaceHover"],
  "muted-foreground": ["textMuted", "faint"],
  border: ["border", "line", "surfacePressed", "track"],
  input: ["fieldBorder"],
  ring: ["focus"],
  sidebar: ["sidebar"],
  orange: ["warning"],
  red: ["danger", "down"],
  green: ["success"],
};

function palette(selector) {
  // These two selectors in styles.css declare literal colors on a single level.
  const blocks = [...css.matchAll(new RegExp(`^${selector}\\s*\\{([^}]+)\\}`, "gm"))];
  const tokens = new Map(blocks.flatMap(([, rules]) => [...rules.matchAll(/--([\w-]+):\s*([^;]+);/g)].map(([, k, v]) => [k, v.trim()])));
  const colors = {};
  for (const [token, names] of Object.entries(roles)) {
    const value = tokens.get(token);
    if (!value) throw new Error(`Missing ${selector} token --${token}`);
    for (const name of names) colors[name] = value;
  }
  return colors;
}

writeFileSync(new URL("theme.json", import.meta.url), JSON.stringify({ light: palette(":root"), dark: palette("\\.dark") }, null, 2) + "\n");
