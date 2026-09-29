#!/usr/bin/env node
/* ===========================================================================
 * opencode-portable / patch-asar.mjs
 * ---------------------------------------------------------------------------
 * Dependency-free ASAR patcher, written so the USB builder does not need npm.
 *
 * It performs the "portable conversion" of the official opencode desktop app:
 *
 *   1. adds out/main/opencode-portable-boot.js (the portable bootstrap that
 *      redirects every writable location to the USB stick);
 *   2. appends a single `import "./opencode-portable-boot.js";` line to
 *      out/main/index.js - ES modules evaluate their imports before the module
 *      body, so the bootstrap runs before any application code;
 *   3. disables the electron-updater (never download/install the NSIS
 *      installer), registers no `opencode://` protocol handler in the Windows
 *      registry, and makes the hard-coded userData path portable-aware.
 *
 * Usage:
 *   node patch-asar.mjs <path/to/app.asar> [--boot <path/to/inject-bootstrap.js>]
 *                        [--dry-run] [--report <file.json>] [--no-backup]
 *
 * Exit code 0 = patched & verified, 1 = failed (original file untouched).
 * ===========================================================================
 */
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import crypto from "node:crypto";
import { spawnSync } from "node:child_process";

const BOOT_NAME = "opencode-portable-boot.js";
const BOOT_ARCHIVE_PATH = `out/main/${BOOT_NAME}`;
const BOOT_IMPORT_LINE = `import "./${BOOT_NAME}"; // opencode-portable`;

// ---------------------------------------------------------------------------
// tiny pickle helpers (Chromium Pickle layout used by ASAR)
// ---------------------------------------------------------------------------
const align4 = (n) => n + ((4 - (n % 4)) % 4);

function readArchive(file) {
  const fd = fs.openSync(file, "r");
  try {
    const sizeBuf = Buffer.alloc(8);
    if (fs.readSync(fd, sizeBuf, 0, 8, null) !== 8) throw new Error("unable to read ASAR header size");
    if (sizeBuf.readUInt32LE(0) !== 4) throw new Error("unexpected ASAR size pickle");
    const headerSize = sizeBuf.readUInt32LE(4);
    if (headerSize <= 0 || headerSize > 64 * 1024 * 1024) throw new Error(`implausible ASAR header size: ${headerSize}`);
    const headerBuf = Buffer.alloc(headerSize);
    if (fs.readSync(fd, headerBuf, 0, headerSize, 8) !== headerSize) throw new Error("unable to read ASAR header");
    const payloadSize = headerBuf.readUInt32LE(0);
    const jsonLength = headerBuf.readUInt32LE(4);
    if (jsonLength > payloadSize) throw new Error("corrupt ASAR header pickle");
    const json = headerBuf.subarray(8, 8 + jsonLength).toString("utf8");
    return { header: JSON.parse(json), headerSize, dataOffset: 8 + headerSize, size: fs.fstatSync(fd).size };
  } finally {
    fs.closeSync(fd);
  }
}

function buildHeaderBuffers(header) {
  const json = Buffer.from(JSON.stringify(header), "utf8");
  const headerBuf = Buffer.alloc(4 + align4(4 + json.length));
  const payloadSize = 4 + align4(json.length);
  headerBuf.writeUInt32LE(payloadSize, 0);
  headerBuf.writeUInt32LE(json.length, 4);
  json.copy(headerBuf, 8);
  const sizeBuf = Buffer.alloc(8);
  sizeBuf.writeUInt32LE(4, 0);
  sizeBuf.writeUInt32LE(headerBuf.length, 4);
  return { sizeBuf, headerBuf };
}

// ---------------------------------------------------------------------------
// file integrity blocks (same algorithm as @electron/asar)
// ---------------------------------------------------------------------------
const BLOCK_SIZE = 4 * 1024 * 1024;

function computeIntegrity(buffer) {
  const blocks = [];
  for (let offset = 0; offset < buffer.length; offset += BLOCK_SIZE) {
    blocks.push(crypto.createHash("sha256").update(buffer.subarray(offset, offset + BLOCK_SIZE)).digest("hex"));
  }
  if (blocks.length === 0) blocks.push(crypto.createHash("sha256").update(Buffer.alloc(0)).digest("hex"));
  return {
    algorithm: "SHA256",
    hash: crypto.createHash("sha256").update(buffer).digest("hex"),
    blockSize: BLOCK_SIZE,
    blocks,
  };
}

// ---------------------------------------------------------------------------
// archive tree helpers
// ---------------------------------------------------------------------------
function eachFile(node, prefix, out) {
  for (const [name, value] of Object.entries(node.files ?? {})) {
    const archivePath = prefix ? `${prefix}/${name}` : name;
    if (value && typeof value === "object" && value.files) {
      eachFile(value, archivePath, out);
    } else if (value && typeof value === "object") {
      out.push({ archivePath, entry: value, padding: name });
    }
  }
  return out;
}

function listFiles(header) {
  return eachFile(header, "", []);
}

// ---------------------------------------------------------------------------
// patch definitions
// ---------------------------------------------------------------------------
const JS_PATCHES = [
  {
    id: "updater-disabled",
    description: "disable electron-updater while running in portable mode",
    find: `const UPDATER_ENABLED = app.isPackaged && CHANNEL !== "dev";`,
    replace: `const UPDATER_ENABLED = app.isPackaged && CHANNEL !== "dev" && process.env.OPENCODE_PORTABLE !== "1";`,
    required: true,
  },
  {
    id: "userdata-portable",
    description: "make the hard-coded userData path honour the portable root",
    find: `onboardingTestRoot ? join(onboardingTestRoot, "desktop") : join(app.getPath("appData"), appId)`,
    replace: `onboardingTestRoot ? join(onboardingTestRoot, "desktop") : (process.env.OPENCODE_PORTABLE_USERDATA || join(app.getPath("appData"), appId))`,
    required: true,
  },
  {
    id: "sessiondata-portable",
    description: "keep the Chromium session/cache data on the stick as well",
    find: `if (onboardingTestRoot) app.setPath("sessionData", join(onboardingTestRoot, "session"));`,
    replace:
      `if (onboardingTestRoot) app.setPath("sessionData", join(onboardingTestRoot, "session"));\n` +
      `  else if (process.env.OPENCODE_PORTABLE_USERDATA) app.setPath("sessionData", join(process.env.OPENCODE_PORTABLE_USERDATA, "session"));`,
    required: true,
  },
  {
    id: "no-protocol-registration",
    description: "never write the opencode:// protocol handler into the Windows registry",
    find: `app.setAsDefaultProtocolClient("opencode");`,
    replace: `if (process.env.OPENCODE_PORTABLE !== "1") app.setAsDefaultProtocolClient("opencode");`,
    required: false, // tolerated if a future build drops it
  },
];

const countOccurrences = (haystack, needle) => haystack.split(needle).length - 1;

function patchSource(source, log) {
  let out = source;
  const applied = [];
  for (const patch of JS_PATCHES) {
    const hits = countOccurrences(out, patch.find);
    if (hits === 0) {
      if (patch.required) throw new Error(`patch "${patch.id}" did not match - the app build changed, refusing to continue`);
      log(`  [warn] optional patch "${patch.id}" did not match (skipped)`);
      applied.push({ ...patch, status: "skipped" });
      continue;
    }
    if (hits > 1) throw new Error(`patch "${patch.id}" matches ${hits} times - ambiguous, refusing to continue`);
    out = out.replace(patch.find, patch.replace);
    applied.push({ ...patch, status: "applied" });
    log(`  [ok]   ${patch.id}`);
  }
  if (!out.includes(BOOT_IMPORT_LINE)) {
    const trimmed = out.endsWith("\n") ? out : `${out}\n`;
    out = `${trimmed}${BOOT_IMPORT_LINE}\n`;
    applied.push({ id: "boot-import", status: "applied" });
    log(`  [ok]   boot-import (appended to out/main/index.js)`);
  }
  return { source: out, applied };
}

function syntaxCheck(source, log) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "ocp-check-"));
  const file = path.join(dir, "check.mjs");
  try {
    fs.writeFileSync(file, source, "utf8");
    const result = spawnSync(process.execPath, ["--check", file], { encoding: "utf8" });
    if (result.status !== 0) throw new Error(`patched out/main/index.js is not valid JavaScript:\n${result.stderr}`);
    log("  [ok]   syntax check of the patched main bundle");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------
function main() {
  const args = process.argv.slice(2);
  const flags = new Set(args.filter((a) => a.startsWith("--")));
  const values = new Map();
  for (let i = 0; i < args.length; i++) {
    if (args[i] === "--boot" || args[i] === "--report") values.set(args[i], args[i + 1]);
  }
  const target = args.find((a) => !a.startsWith("--") && a !== values.get("--boot") && a !== values.get("--report"));
  const log = (message) => console.log(message);

  if (!target) {
    console.error("usage: node patch-asar.mjs <path/to/app.asar> [--boot <file>] [--dry-run] [--report <file>]");
    process.exit(2);
  }
  const archivePath = path.resolve(target);
  if (!fs.existsSync(archivePath)) {
    console.error(`[error] not found: ${archivePath}`);
    process.exit(2);
  }

  const bootFile = values.get("--boot");
  if (!bootFile || !fs.existsSync(bootFile)) {
    console.error(`[error] bootstrap file missing: ${bootFile ?? "<none>"} (pass --boot <path>)`);
    process.exit(2);
  }
  const bootSource = fs.readFileSync(bootFile, "utf8");
  const dryRun = flags.has("--dry-run");
  const report = { archive: archivePath, boot: bootFile, applied: [], files: 0, bytes: 0 };

  log(`[1/5] reading  ${archivePath}`);
  const { header, headerSize, dataOffset, size } = readArchive(archivePath);
  const files = listFiles(header);
  report.files = files.length;
  report.bytes = size;
  log(`      ${(size / 1048576).toFixed(1)} MB, ${files.length} files, header ${(headerSize / 1024).toFixed(0)} KB`);

  const mainEntry = files.find((f) => f.archivePath === "out/main/index.js");
  if (!mainEntry) throw new Error("out/main/index.js not found - is this really an opencode desktop app.asar?");

  log("[2/5] patching out/main/index.js");
  const fd = fs.openSync(archivePath, "r");
  let originalSource;
  try {
    const buffer = Buffer.alloc(mainEntry.entry.size);
    fs.readSync(fd, buffer, 0, mainEntry.entry.size, dataOffset + Number(mainEntry.entry.offset));
    originalSource = buffer.toString("utf8");
  } finally {
    fs.closeSync(fd);
  }
  const { source: patchedSource, applied } = patchSource(originalSource, log);
  report.applied = applied.map((p) => ({ id: p.id, status: p.status }));
  syntaxCheck(patchedSource, log);

  if (dryRun) {
    log("[dry-run] stopping before writing");
    if (values.get("--report")) fs.writeFileSync(values.get("--report"), JSON.stringify(report, null, 2));
    return;
  }

  log("[3/5] rebuilding the archive");
  const outDir = path.dirname(archivePath);
  const tempPath = path.join(outDir, `app.asar.portable-${process.pid}.tmp`);
  const bootBuffer = Buffer.from(bootSource, "utf8");
  const patchedBuffer = Buffer.from(patchedSource, "utf8");

  // new archive tree: same structure, plus the bootstrap file
  const newHeader = JSON.parse(JSON.stringify(header));
  const outMain = (() => {
    for (const f of listFiles(newHeader)) if (f.archivePath === "out/main/index.js") return f.entry;
    return undefined;
  })();
  let cursor = 0n;
  const replacements = new Map([[mainEntry.archivePath, patchedBuffer]]);
  if (!listFiles(newHeader).some((f) => f.archivePath === BOOT_ARCHIVE_PATH)) {
    const parent = (() => {
      const parts = BOOT_ARCHIVE_PATH.split("/");
      let node = newHeader;
      for (const part of parts.slice(0, -1)) {
        node.files[part] ??= { files: {} };
        node = node.files[part];
      }
      return node;
    })();
    parent.files[BOOT_NAME] = { size: bootBuffer.length };
    replacements.set(BOOT_ARCHIVE_PATH, bootBuffer);
  } else {
    // keep size in sync for a re-run
    for (const f of listFiles(newHeader)) if (f.archivePath === BOOT_ARCHIVE_PATH) f.entry.size = bootBuffer.length;
    replacements.set(BOOT_ARCHIVE_PATH, bootBuffer);
  }

  const entries = listFiles(newHeader);
  for (const { archivePath: p, entry } of entries) {
    if (entry.unpacked) continue; // lives in app.asar.unpacked, untouched
    const data = replacements.get(p);
    const length = data ? data.length : entry.size;
    entry.size = length;
    entry.offset = String(cursor);
    if (data && entry.integrity) entry.integrity = computeIntegrity(data);
    cursor += BigInt(length);
  }

  const { sizeBuf, headerBuf } = buildHeaderBuffers(newHeader);
  const out = fs.openSync(tempPath, "w");
  const src = fs.openSync(archivePath, "r");
  try {
    fs.writeSync(out, sizeBuf);
    fs.writeSync(out, headerBuf);
    const chunk = Buffer.alloc(4 * 1024 * 1024);
    for (const { archivePath: p, entry } of entries) {
      const data = replacements.get(p);
      if (data) {
        fs.writeSync(out, data);
        continue;
      }
      if (entry.unpacked || entry.size <= 0) continue;
      const original = files.find((f) => f.archivePath === p);
      if (!original) throw new Error(`entry disappeared during rebuild: ${p}`);
      let remaining = entry.size;
      let position = dataOffset + Number(original.entry.offset);
      while (remaining > 0) {
        const wanted = Math.min(remaining, chunk.length);
        const read = fs.readSync(src, chunk, 0, wanted, position);
        if (read <= 0) throw new Error(`short read while copying ${p}`);
        fs.writeSync(out, chunk, 0, read);
        remaining -= read;
        position += read;
      }
    }
  } finally {
    fs.closeSync(src);
    fs.closeSync(out);
  }
  report.newArchiveBytes = fs.statSync(tempPath).size;
  log(`      ${(report.newArchiveBytes / 1048576).toFixed(1)} MB written to a temporary file`);

  log("[4/5] verifying the new archive");
  const check = readArchive(tempPath);
  const checkFiles = listFiles(check.header);
  const checkMain = checkFiles.find((f) => f.archivePath === "out/main/index.js");
  const checkBoot = checkFiles.find((f) => f.archivePath === BOOT_ARCHIVE_PATH);
  if (!checkMain || !checkBoot) throw new Error("verification failed: rebuilt archive is missing files");
  const verifyFd = fs.openSync(tempPath, "r");
  try {
    const buffer = Buffer.alloc(checkBoot.entry.size);
    fs.readSync(verifyFd, buffer, 0, checkBoot.entry.size, check.dataOffset + Number(checkBoot.entry.offset));
    if (crypto.createHash("sha256").update(buffer).digest("hex") !== crypto.createHash("sha256").update(bootBuffer).digest("hex"))
      throw new Error("verification failed: bootstrap content mismatch");
  } finally {
    fs.closeSync(verifyFd);
  }
  if (checkFiles.length !== files.length + (files.some((f) => f.archivePath === BOOT_ARCHIVE_PATH) ? 0 : 1))
    throw new Error("verification failed: file count changed unexpectedly");
  log(`      ${checkFiles.length} files, header and file table consistent`);

  log("[5/5] installing the patched archive");
  const backup = flags.has("--no-backup") ? undefined : `${archivePath}.orig`;
  if (backup) fs.copyFileSync(archivePath, backup);
  try {
    fs.renameSync(tempPath, archivePath);
  } catch (error) {
    fs.copyFileSync(tempPath, archivePath);
    fs.rmSync(tempPath, { force: true });
  }
  if (backup) log(`      original kept at ${path.basename(backup)} (delete it once satisfied)`);
  log("done - the app now runs in portable mode.");

  if (values.get("--report")) {
    report.backup = backup;
    fs.writeFileSync(values.get("--report"), JSON.stringify(report, null, 2));
  }
}

try {
  main();
} catch (error) {
  console.error(`\n[error] ${error instanceof Error ? error.message : String(error)}`);
  process.exit(1);
}
