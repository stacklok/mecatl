#!/usr/bin/env node

import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const defaultOutput = resolve(projectRoot, "../../user-docs/reference/typescript-sdk-api");

function parseArguments(arguments_) {
  let output = defaultOutput;
  for (let index = 0; index < arguments_.length; index += 1) {
    const argument = arguments_[index];
    if (argument !== "--output") {
      throw new Error(`unknown argument: ${argument}`);
    }
    const value = arguments_[index + 1];
    if (value === undefined || value === "") {
      throw new Error("--output requires a directory");
    }
    output = resolve(process.cwd(), value);
    index += 1;
  }
  return { output };
}

function excerpt(item) {
  return (item.excerptTokens ?? [])
    .map((token) => token.text)
    .join("")
    .trim();
}

function publicName(item) {
  const match = excerpt(item).match(
    /\b(?:class|const|enum|function|interface|namespace|type|var|let)\s+([A-Za-z_$][\w$]*)/u,
  );
  return match?.[1] ?? item.name.replace(/_\d+$/u, "");
}

function cleanInline(text) {
  return text
    .replace(/\{@link\s+([^}|\s]+)(?:\s*\|\s*([^}]+))?\}/gu, (_match, target, label) =>
      label === undefined ? `\`${target}\`` : label.trim(),
    )
    .replace(/\s+/gu, " ")
    .trim();
}

function parseDocComment(raw) {
  if (raw === undefined || raw.trim() === "") {
    return { params: new Map(), remarks: "", returns: "", summary: "", throws: [] };
  }

  const lines = raw
    .replace(/^\s*\/\*\*\s*/u, "")
    .replace(/\s*\*\/\s*$/u, "")
    .split(/\r?\n/u)
    .map((line) => line.replace(/^\s*\* ?/u, "").trimEnd());

  const result = { params: new Map(), remarks: "", returns: "", summary: "", throws: [] };
  let target = "summary";
  let parameter = "";
  for (const line of lines) {
    const tag = line.match(/^@(\w+)\b\s*(.*)$/u);
    if (tag !== null) {
      const [, name, value] = tag;
      if (name === "param") {
        const match = value.match(/^([^\s]+)\s*(?:-\s*)?(.*)$/u);
        if (match !== null) {
          parameter = match[1];
          result.params.set(parameter, match[2]);
          target = "param";
        }
      } else if (name === "returns") {
        result.returns = value;
        target = "returns";
      } else if (name === "remarks") {
        result.remarks = value;
        target = "remarks";
      } else if (name === "throws") {
        result.throws.push(value);
        target = "throws";
      } else {
        target = "ignored";
      }
      continue;
    }

    if (line.trim() === "") {
      if (target === "summary" && result.summary !== "") result.summary += "\n\n";
      if (target === "remarks" && result.remarks !== "") result.remarks += "\n\n";
      continue;
    }

    if (target === "summary" || target === "remarks" || target === "returns") {
      result[target] += `${result[target] === "" ? "" : " "}${line}`;
    } else if (target === "param" && parameter !== "") {
      const current = result.params.get(parameter) ?? "";
      result.params.set(parameter, `${current}${current === "" ? "" : " "}${line}`);
    } else if (target === "throws" && result.throws.length > 0) {
      const last = result.throws.length - 1;
      result.throws[last] = `${result.throws[last]} ${line}`;
    }
  }

  result.summary = cleanInline(result.summary);
  result.remarks = cleanInline(result.remarks);
  result.returns = cleanInline(result.returns);
  result.throws = result.throws.map(cleanInline);
  result.params = new Map([...result.params].map(([name, value]) => [name, cleanInline(value)]));
  return result;
}

function loadEntryPoint(raw, source) {
  if (raw.kind !== "Package" || raw.members?.length !== 1 || raw.members[0].kind !== "EntryPoint") {
    throw new Error(`${source} does not contain exactly one API Extractor entry point`);
  }
  return raw.members[0].members ?? [];
}

function isCallable(item) {
  return new Set([
    "Constructor",
    "ConstructSignature",
    "Function",
    "Method",
    "MethodSignature",
  ]).has(item.kind);
}

function validateDocumentation(items, entryPoint) {
  const missing = [];
  const detailedCallableTypes = new Set([
    "AttachedRun",
    "NodeClient",
    "PlanResolution",
    "Run",
    "Session",
  ]);
  const documentedMemberTypes = new Set(["MediaPartSource", "SessionLimits", "SessionMcpServer"]);
  const propertyKinds = new Set(["Property", "PropertySignature"]);
  for (const item of items) {
    const itemName = publicName(item);
    const itemDoc = parseDocComment(item.docComment);
    if (itemDoc.summary === "") {
      missing.push(itemName);
    }
    if (item.kind === "Function") validateCallableDetails(item, itemName, itemDoc, missing);
    for (const member of item.members ?? []) {
      const propertyNeedsDocumentation =
        (itemName.endsWith("Options") || documentedMemberTypes.has(itemName)) &&
        propertyKinds.has(member.kind);
      if (
        (isCallable(member) || propertyNeedsDocumentation) &&
        parseDocComment(member.docComment).summary === ""
      ) {
        const memberName = member.kind === "Constructor" ? "constructor" : member.name;
        missing.push(`${itemName}.${memberName}`);
      }
      if (detailedCallableTypes.has(itemName) && isCallable(member)) {
        const memberName = member.kind === "Constructor" ? "constructor" : member.name;
        validateCallableDetails(
          member,
          `${itemName}.${memberName}`,
          parseDocComment(member.docComment),
          missing,
        );
      }
    }
  }
  if (missing.length > 0) {
    throw new Error(
      `${entryPoint} has undocumented public declarations or callables:\n${missing.map((name) => `- ${name}`).join("\n")}`,
    );
  }
}

function validateCallableDetails(item, name, doc, missing) {
  for (const parameter of item.parameters ?? []) {
    if ((doc.params.get(parameter.parameterName) ?? "") === "") {
      missing.push(`${name} parameter ${parameter.parameterName}`);
    }
  }
  if (item.returnTypeTokenRange !== undefined && doc.returns === "") {
    missing.push(`${name} return value`);
  }
}

const groupOrder = ["Class", "Function", "Interface", "TypeAlias", "Enum", "Variable", "Namespace"];
const groupTitles = {
  Class: "Classes",
  Enum: "Enumerations",
  Function: "Functions",
  Interface: "Interfaces",
  Namespace: "Namespaces",
  TypeAlias: "Type aliases",
  Variable: "Variables",
};
const groupKinds = {
  Class: "Class",
  Enum: "Enumeration",
  Function: "Function",
  Interface: "Interface",
  Namespace: "Namespace",
  TypeAlias: "Type alias",
  Variable: "Variable",
};

function anchorPart(value) {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/gu, "-")
    .replace(/^-|-$/gu, "");
}

function escapeHtml(value) {
  return value.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");
}

function itemAnchor(item) {
  const overload = item.overloadIndex > 1 ? `-overload-${item.overloadIndex}` : "";
  return `api-${anchorPart(publicName(item))}-${anchorPart(item.kind)}${overload}`;
}

function memberAnchor(item, member) {
  const memberName = member.kind === "Constructor" ? "constructor" : member.name;
  const overload = member.overloadIndex > 1 ? `-overload-${member.overloadIndex}` : "";
  return `api-${anchorPart(publicName(item))}-${anchorPart(memberName)}-${anchorPart(member.kind)}${overload}`;
}

function memberDisplayName(item, member) {
  const owner = publicName(item);
  const memberName = member.kind === "Constructor" ? "constructor" : member.name;
  if (memberName.startsWith("[")) return `${owner}${memberName}`;
  if (memberName.startsWith('"')) return `${owner}[${memberName}]`;
  return `${owner}.${memberName}`;
}

function renderSymbolIndex(items) {
  const output = ["## Symbol index", "", "| Symbol | Kind |", "| --- | --- |"];
  const sorted = [...items].sort((left, right) =>
    publicName(left).localeCompare(publicName(right)),
  );
  for (const item of sorted) {
    output.push(`| [\`${publicName(item)}\`](#${itemAnchor(item)}) | ${groupKinds[item.kind]} |`);
  }
  output.push("");
  return output;
}

function renderNarrative(doc) {
  const output = [];
  if (doc.summary !== "") output.push(doc.summary, "");
  if (doc.remarks !== "") output.push(doc.remarks, "");
  return output;
}

function tokenRange(item, range) {
  if (range === undefined) return "";
  return (item.excerptTokens ?? [])
    .slice(range.startIndex, range.endIndex)
    .map((token) => token.text)
    .join("")
    .replace(/\s+/gu, " ")
    .trim();
}

function renderCallableDetails(item, doc) {
  const output = [];
  if ((item.parameters ?? []).length > 0) {
    output.push("Parameters:", "");
    for (const parameter of item.parameters) {
      const type = tokenRange(item, parameter.parameterTypeTokenRange);
      const optional = parameter.isOptional ? ", optional" : "";
      const description = doc.params.get(parameter.parameterName) ?? "";
      output.push(
        `- \`${parameter.parameterName}\` (\`${type}\`${optional})${description === "" ? "" : `: ${description}`}`,
      );
    }
    output.push("");
  }

  const returnType = tokenRange(item, item.returnTypeTokenRange);
  if (returnType !== "") {
    output.push(`Returns: \`${returnType}\`${doc.returns === "" ? "" : `: ${doc.returns}`}`, "");
  }
  for (const description of doc.throws) output.push(`Throws: ${description}`, "");
  return output;
}

function renderItem(item) {
  const doc = parseDocComment(item.docComment);
  const output = [
    `<Heading as="h3" id="${itemAnchor(item)}"><code>${escapeHtml(publicName(item))}</code></Heading>`,
    "",
    ...renderNarrative(doc),
  ];
  const signature = excerpt(item);
  if (signature !== "") output.push("```ts", signature, "```", "");
  if (isCallable(item)) output.push(...renderCallableDetails(item, doc));

  const members = [...(item.members ?? [])].sort((left, right) => {
    const leftName = left.kind === "Constructor" ? "constructor" : (left.name ?? "");
    const rightName = right.kind === "Constructor" ? "constructor" : (right.name ?? "");
    const name = leftName.localeCompare(rightName);
    return name === 0 ? left.kind.localeCompare(right.kind) : name;
  });
  const callableMembers = members.filter(isCallable);
  if (callableMembers.length > 0) {
    output.push(
      `Callable members: ${callableMembers
        .map((member) => {
          const suffix = member.overloadIndex > 1 ? ` (overload ${member.overloadIndex})` : "";
          const name = member.kind === "Constructor" ? "constructor" : `${member.name}()`;
          return `[\`${name}\`${suffix}](#${memberAnchor(item, member)})`;
        })
        .join(", ")}`,
      "",
    );
  }
  for (const member of members) {
    const suffix = member.overloadIndex > 1 ? ` (overload ${member.overloadIndex})` : "";
    const memberDoc = parseDocComment(member.docComment);
    output.push(
      `<Heading as="h4" id="${memberAnchor(item, member)}"><code>${escapeHtml(memberDisplayName(item, member))}</code>${suffix}</Heading>`,
      "",
      ...renderNarrative(memberDoc),
    );
    const memberSignature = excerpt(member);
    if (memberSignature !== "") output.push("```ts", memberSignature, "```", "");
    if (isCallable(member)) output.push(...renderCallableDetails(member, memberDoc));
  }
  return output;
}

function renderReference({ description, entryPoint, items, position, sharedReference, title }) {
  const output = [
    "---",
    `title: ${title}`,
    `description: ${description}`,
    `sidebar_position: ${position}`,
    "toc_max_heading_level: 2",
    "---",
    "",
    'import Heading from "@theme/Heading";',
    "",
    `# ${title}`,
    "",
    "{/* Generated from API Extractor models and sdk/typescript/src TSDoc. Regenerate with task sdk:docs. DO NOT EDIT. */}",
    "",
    sharedReference === undefined
      ? `This reference describes the declarations exported by \`${entryPoint}\`.`
      : `This page lists declarations added or changed by \`${entryPoint}\`. The entry point also exports the [shared core API](${sharedReference}).`,
    "",
    ...renderSymbolIndex(items),
  ];

  const grouped = new Map();
  for (const kind of groupOrder) grouped.set(kind, []);
  for (const item of items) {
    const group = grouped.get(item.kind);
    if (group === undefined) throw new Error(`unsupported API item kind: ${item.kind}`);
    group.push(item);
  }
  for (const kind of groupOrder) {
    const group = grouped.get(kind);
    if (group.length === 0) continue;
    output.push(`## ${groupTitles[kind]}`, "");
    group.sort((left, right) => publicName(left).localeCompare(publicName(right)));
    for (const item of group) output.push(...renderItem(item));
  }
  return `${output.join("\n").trim()}\n`;
}

const { output } = parseArguments(process.argv.slice(2));
const coreModelPath = resolve(projectRoot, ".api-extractor-temp/models/core/mecatl-sdk.api.json");
const nodeModelPath = resolve(
  projectRoot,
  ".api-extractor-temp/models/node/mecatl-sdk-node.api.json",
);
const [coreModel, nodeModel] = await Promise.all([
  readFile(coreModelPath, "utf8").then(JSON.parse),
  readFile(nodeModelPath, "utf8").then(JSON.parse),
]);
const coreItems = loadEntryPoint(coreModel, coreModelPath);
const nodeItems = loadEntryPoint(nodeModel, nodeModelPath);
const coreByReference = new Map(coreItems.map((item) => [item.canonicalReference, item]));
const nodeSpecificItems = nodeItems.filter((item) => {
  const core = coreByReference.get(item.canonicalReference);
  return core === undefined || excerpt(core) !== excerpt(item);
});

validateDocumentation(coreItems, "@stacklok-oss/mecatl-sdk");
validateDocumentation(nodeSpecificItems, "@stacklok-oss/mecatl-sdk/node");
await mkdir(output, { recursive: true });
await Promise.all([
  writeFile(
    resolve(output, "core.md"),
    renderReference({
      description:
        "Look up the transport-neutral TypeScript SDK functions, methods, types, and errors.",
      entryPoint: "@stacklok-oss/mecatl-sdk",
      items: coreItems,
      position: 2,
      title: "TypeScript SDK core API",
    }),
  ),
  writeFile(
    resolve(output, "node.md"),
    renderReference({
      description: "Look up the Node.js and Bun TypeScript SDK functions, methods, and types.",
      entryPoint: "@stacklok-oss/mecatl-sdk/node",
      items: nodeSpecificItems,
      position: 3,
      sharedReference: "./core.md",
      title: "TypeScript SDK Node.js and Bun API",
    }),
  ),
]);

process.stdout.write(`generated ${resolve(output, "core.md")}\n`);
process.stdout.write(`generated ${resolve(output, "node.md")}\n`);
