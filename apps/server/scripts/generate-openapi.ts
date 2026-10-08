// SPDX-License-Identifier: Apache-2.0

import { writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createApp, openApiInfo } from "../src/app.js";

const output = fileURLToPath(new URL("../../contracts/openapi.json", import.meta.url));
const app = createApp();
const document = app.getOpenAPIDocument(openApiInfo);

await writeFile(output, `${JSON.stringify(document, null, 2)}\n`, "utf8");
console.log(`Wrote ${output}`);
