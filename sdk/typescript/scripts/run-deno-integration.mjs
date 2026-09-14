import { execFileSync } from "node:child_process";
import { copyFile, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

const sdkRoot = fileURLToPath(new URL("../", import.meta.url));
const repositoryRoot = fileURLToPath(new URL("../../../", import.meta.url));
const scratchRoot = join(repositoryRoot, ".scratch");
const binary = join(repositoryRoot, "bin", "mecated");
const deno = process.env.DENO_BIN || "deno";
const manifest = JSON.parse(await readFile(join(sdkRoot, "package.json"), "utf8"));
const archiveName = `${manifest.name.replace(/^@/u, "").replaceAll("/", "-")}-${manifest.version}.tgz`;
const archive = process.env.MECATL_SDK_PACKED_TARBALL
  ? resolve(sdkRoot, process.env.MECATL_SDK_PACKED_TARBALL)
  : join(sdkRoot, ".api-extractor-temp", archiveName);

await mkdir(scratchRoot, { recursive: true });
const consumer = await mkdtemp(join(scratchRoot, "sdk-deno-consumer-"));
try {
  const installedRoot = join(consumer, "node_modules", ...manifest.name.split("/"));
  await mkdir(installedRoot, { recursive: true });
  execFileSync("tar", ["-xzf", archive, "--strip-components=1", "-C", installedRoot]);
  const packedManifest = JSON.parse(await readFile(join(installedRoot, "package.json"), "utf8"));
  for (const dependency of Object.keys(packedManifest.dependencies ?? {})) {
    const destination = join(consumer, "node_modules", ...dependency.split("/"));
    await mkdir(dirname(destination), { recursive: true });
    await symlink(join(sdkRoot, "node_modules", ...dependency.split("/")), destination, "junction");
  }
  await writeFile(
    join(consumer, "package.json"),
    JSON.stringify({
      type: "module",
      dependencies: { [packedManifest.name]: packedManifest.version },
    }),
  );
  await Promise.all([
    copyFile(join(sdkRoot, "e2e", "deno.json"), join(consumer, "deno.json")),
    copyFile(join(sdkRoot, "e2e", "deno.e2e.ts"), join(consumer, "deno.e2e.ts")),
    copyFile(join(sdkRoot, "e2e", "deno-grpc.e2e.ts"), join(consumer, "deno-grpc.e2e.ts")),
    copyFile(join(sdkRoot, "e2e", "deno-owner.ts"), join(consumer, "deno-owner.ts")),
    ...["deno-local.ts", "deno-remote.ts"].map((name) =>
      copyFile(join(sdkRoot, "examples", name), join(consumer, name)),
    ),
  ]);

  // Run outside the SDK tree: bare imports must resolve the extracted tarball.
  execFileSync(
    deno,
    [
      "check",
      "--config",
      "deno.json",
      "deno.e2e.ts",
      "deno-grpc.e2e.ts",
      "deno-owner.ts",
      "deno-local.ts",
      "deno-remote.ts",
    ],
    { cwd: consumer, stdio: "inherit" },
  );
  const runArguments = [
    "run",
    "--config",
    "deno.json",
    `--allow-read=${repositoryRoot}`,
    `--allow-write=${scratchRoot}`,
    `--allow-run=${binary}`,
    "--allow-net=127.0.0.1",
  ];
  execFileSync(deno, [...runArguments, "deno.e2e.ts", binary, scratchRoot], {
    cwd: consumer,
    stdio: "inherit",
    timeout: 60_000,
  });

  // Create an ephemeral private CA and a signed loopback server certificate. No TLS
  // verification bypass or checked-in private key is needed by the native Deno test.
  const certificates = join(consumer, "certificates");
  await mkdir(certificates);
  const openssl = (args) =>
    execFileSync("openssl", args, {
      cwd: certificates,
      stdio: ["ignore", "ignore", "pipe"],
      timeout: 15_000,
    });
  openssl([
    "req",
    "-x509",
    "-newkey",
    "rsa:2048",
    "-nodes",
    "-days",
    "1",
    "-subj",
    "/CN=Mecatl Deno test CA",
    "-keyout",
    "ca-key.pem",
    "-out",
    "ca.pem",
  ]);
  openssl([
    "req",
    "-new",
    "-newkey",
    "rsa:2048",
    "-nodes",
    "-subj",
    "/CN=localhost",
    "-keyout",
    "server-key.pem",
    "-out",
    "server.csr",
  ]);
  await writeFile(
    join(certificates, "server.ext"),
    "subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n",
  );
  openssl([
    "x509",
    "-req",
    "-in",
    "server.csr",
    "-CA",
    "ca.pem",
    "-CAkey",
    "ca-key.pem",
    "-CAcreateserial",
    "-days",
    "1",
    "-extfile",
    "server.ext",
    "-out",
    "server.pem",
  ]);
  for (const mode of ["tcp", "tls", "uds"]) {
    // UDS paths must be absolute and fit sockaddr_un.sun_path. Avoid nesting them
    // beneath the longer packed-consumer path; keep all fixtures in repo scratch.
    const directory = await mkdtemp(join(scratchRoot, "dg-"));
    try {
      await Promise.all([
        readFile(join(sdkRoot, "e2e", "fixtures", "cancel.json"), "utf8").then((source) => {
          const script = JSON.parse(source);
          return writeFile(
            join(directory, "cancel.json"),
            JSON.stringify({ turns: [...script.turns, ...script.turns] }),
          );
        }),
        ...["ca.pem", "server.pem", "server-key.pem"].map((name) =>
          copyFile(join(certificates, name), join(directory, name)),
        ),
      ]);
      // Timeout makes leaked HTTP/2 sessions or unresolved cancellation fail the gate.
      execFileSync(deno, [...runArguments, "deno-grpc.e2e.ts", binary, directory, mode], {
        cwd: consumer,
        stdio: "inherit",
        timeout: 45_000,
      });
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  }

  const ownerDirectory = join(consumer, "owner-runtime");
  await mkdir(ownerDirectory);
  const { pid } = JSON.parse(
    execFileSync(deno, [...runArguments, "deno-owner.ts", binary, ownerDirectory], {
      cwd: consumer,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "inherit"],
      timeout: 45_000,
    }),
  );
  if (!Number.isSafeInteger(pid) || pid <= 0) throw new Error("Invalid owner fixture daemon pid");
  const deadline = Date.now() + 15_000;
  for (;;) {
    try {
      process.kill(pid, 0);
    } catch (error) {
      if (error.code === "ESRCH") break;
      throw error;
    }
    if (Date.now() >= deadline) {
      process.kill(pid, "SIGKILL");
      throw new Error("Deno owner exit left its daemon running after stdin EOF");
    }
    await delay(20);
  }
  console.log("Deno owner-exit stdin EOF cleanup passed");
} finally {
  await rm(consumer, { recursive: true, force: true });
}
