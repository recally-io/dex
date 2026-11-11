// CommonMark opens and closes "**" only on flanking delimiter runs. A run next to CJK
// punctuation often fails that rule: in "**限制：**这篇" the closing run follows "：" and is
// followed by "这", so it cannot close, and the reader sees the asterisks. Models write this
// form often. Before the parse, strongText swaps such a pair for private-use marks. After the
// parse, remarkStrong turns the marked text into a strong node.

const open = "";
const close = "";

// The content holds no markup and no line break, so the parse keeps it as one text node.
const pair = /\*\*(?![\s*])([^*`[\]<_~\\\n]+?)(?<!\s)\*\*/g;

const space = (char: string | undefined) => char === undefined || /\s/u.test(char);
const punct = (char: string | undefined) => char !== undefined && /[\p{P}\p{S}]/u.test(char);

// CommonMark: a run is left-flanking if the next character is not whitespace, and is not
// punctuation unless the previous character is whitespace or punctuation. Right-flanking mirrors it.
const flanks = (before: string | undefined, after: string | undefined) =>
  !space(after) && (!punct(after) || space(before) || punct(before));

function line(text: string) {
  // Code spans stay as typed. Only the parts between them change.
  return text
    .split(/(`+[^`]*`+)/)
    .map((part, index) =>
      index % 2
        ? part
        : part.replace(pair, (match, inner: string, at: number) => {
            const end = at + match.length;

            const opens = flanks(part[at - 1], part[at + 2]);
            const closes = flanks(part[end], part[end - 3]);

            return opens && closes ? match : `${open}${inner}${close}`;
          }),
    )
    .join("");
}

/** Marks each "**" pair that CommonMark would leave as text. Fenced code stays as typed. */
export function strongText(text: string) {
  let fence = false;

  return text
    .split("\n")
    .map((row) => {
      if (/^\s*(```|~~~)/.test(row)) {
        fence = !fence;

        return row;
      }

      return fence ? row : line(row);
    })
    .join("\n");
}

type Node = { type: string; value?: string; children?: Node[] };

function split(value: string): Node[] {
  return value.split(new RegExp(`(${open}[^${close}]*${close})`)).flatMap((part): Node[] => {
    if (!part) return [];

    return part.startsWith(open) && part.endsWith(close)
      ? [{ type: "strong", children: [{ type: "text", value: part.slice(1, -1) }] }]
      : [{ type: "text", value: part.replaceAll(open, "**").replaceAll(close, "**") }];
  });
}

function walk(node: Node) {
  // Code, HTML and other literal nodes show their value as typed.
  if (node.value !== undefined) {
    node.value = node.value.replaceAll(open, "**").replaceAll(close, "**");

    return;
  }

  if (!node.children) return;

  node.children = node.children.flatMap((child) => {
    if (child.type === "text" && child.value?.includes(open)) return split(child.value);

    walk(child);

    return [child];
  });
}

/** The remark plugin that turns the marks of strongText into strong nodes. */
export function remarkStrong() {
  return (tree: Node) => walk(tree);
}
