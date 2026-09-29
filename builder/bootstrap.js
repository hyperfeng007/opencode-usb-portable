/* ===========================================================================
 * OpenCode Portable bootstrap  (injected into out/main/index.js)
 * ---------------------------------------------------------------------------
 * Runs as the very first statements of the Electron main process, before the
 * application's own code executes.
 *
 * What it does:
 *   1. locates the portable root (the folder on the USB stick that contains
 *      OpenCode-Portable.json), either from the OPENCODE_PORTABLE_ROOT
 *      environment variable or by walking up from the executable path;
 *   2. creates every writable directory under <root>\data\ and points ALL of
 *      Electron / opencode's writable locations at them
 *      (userData, sessionData, crashDumps, temp, XDG_* , HOME, APPDATA,
 *      LOCALAPPDATA, TMP/TEMP);
 *   3. switches the application into portable mode (no protocol registration,
 *      no auto-updater - see the accompanying source patches in
 *      patch-asar.mjs);
 *   4. keeps a "host residue" ledger, so that if anything ever escapes to the
 *      Windows profile it is recorded while we are running and removed the
 *      next time the stick is plugged in.
 *
 * Nothing outside the USB stick is written to on a normal run.
 * ===========================================================================
 */
// This module is added as out/main/opencode-portable-boot.js and imported from
// the application's main bundle, so it is loaded as its own ES module: the
// bindings it uses must be imported explicitly (they are NOT globals here).
import { app } from "electron";
import { spawnSync } from "node:child_process";

const __ocpMarkers = ["OpenCode-Portable.json", ".opencode-portable"];
const __ocpLooksLikeRoot = (dir) => {
  if (!dir) return false;
  for (const name of __ocpMarkers) {
    try {
      if (existsSync(join(dir, name))) return true;
    } catch {}
  }
  return false;
};

const __ocpRoot = (() => {
  const fromEnv = process.env.OPENCODE_PORTABLE_ROOT;
  if (fromEnv && __ocpLooksLikeRoot(fromEnv)) return fromEnv;

  const starts = [];
  try {
    starts.push(dirname(process.execPath));
  } catch {}
  try {
    starts.push(dirname(fileURLToPath(import.meta.url)));
  } catch {}

  for (const start of starts) {
    let dir = start;
    for (let depth = 0; depth < 8; depth++) {
      if (__ocpLooksLikeRoot(dir)) return dir;
      const parent = dirname(dir);
      if (!parent || parent === dir) break;
      dir = parent;
    }
  }
  return undefined;
})();

if (__ocpRoot) {
  // --- capture the real Windows locations BEFORE we redirect anything ------
  const __ocpHost = {
    appData: (() => {
      try {
        return app.getPath("appData");
      } catch {
        return process.env.APPDATA;
      }
    })(),
    localAppData: process.env.LOCALAPPDATA,
    temp: process.env.TEMP || process.env.TMP || tmpdir(),
    home: (() => {
      try {
        return homedir();
      } catch {
        return undefined;
      }
    })(),
  };

  const __ocpData = join(__ocpRoot, "data");
  const __ocpDirs = {
    userdata: join(__ocpData, "userdata"),
    session: join(__ocpData, "userdata", "session"),
    crash: join(__ocpData, "userdata", "Crashpad"),
    config: join(__ocpData, "config"),
    data: join(__ocpData, "data"),
    cache: join(__ocpData, "cache"),
    state: join(__ocpData, "state"),
    home: join(__ocpData, "home"),
    tmp: join(__ocpData, "tmp"),
    roaming: join(__ocpData, "roaming"),
    local: join(__ocpData, "local"),
  };
  for (const dir of Object.values(__ocpDirs)) {
    try {
      mkdirSync(dir, { recursive: true });
    } catch {}
  }

  // --- redirect every writable location to the stick ----------------------
  const __ocpEnv = process.env;
  __ocpEnv.OPENCODE_PORTABLE = "1";
  __ocpEnv.OPENCODE_PORTABLE_ROOT = __ocpRoot;
  __ocpEnv.OPENCODE_PORTABLE_USERDATA = __ocpDirs.userdata;
  __ocpEnv.XDG_CONFIG_HOME = __ocpDirs.config;
  __ocpEnv.XDG_DATA_HOME = __ocpDirs.data;
  __ocpEnv.XDG_CACHE_HOME = __ocpDirs.cache;
  __ocpEnv.XDG_STATE_HOME = __ocpDirs.state;
  __ocpEnv.HOME = __ocpDirs.home;
  __ocpEnv.TMP = __ocpDirs.tmp;
  __ocpEnv.TEMP = __ocpDirs.tmp;
  __ocpEnv.APPDATA = __ocpDirs.roaming;
  __ocpEnv.LOCALAPPDATA = __ocpDirs.local;
  __ocpEnv.OPENCODE_DISABLE_AUTOUPDATE = "1";
  if (!__ocpEnv.OPENCODE_CLIENT) __ocpEnv.OPENCODE_CLIENT = "desktop";

  for (const [name, value] of [
    ["userData", __ocpDirs.userdata],
    ["sessionData", __ocpDirs.session],
    ["crashDumps", __ocpDirs.crash],
    ["temp", __ocpDirs.tmp],
  ]) {
    try {
      app.setPath(name, value);
    } catch (error) {
      // a missing directory makes Electron throw - it has been created above,
      // so reaching this branch means something is genuinely wrong; keep going
      // rather than refusing to start.
    }
  }

  // --- host residue ledger -------------------------------------------------
  const __ocpBaselineFile = join(__ocpData, "host-baseline.json");
  const __ocpResidueFile = join(__ocpData, "host-residue.json");
  const __ocpCandidates = () => {
    const out = [];
    const add = (value) => {
      if (value) out.push(value);
    };
    const appIds = ["ai.opencode.desktop", "ai.opencode.desktop.dev", "ai.opencode.desktop.beta"];
    for (const id of appIds) {
      add(__ocpHost.appData && join(__ocpHost.appData, id));
      add(__ocpHost.localAppData && join(__ocpHost.localAppData, id));
    }
    for (const name of ["OpenCode", "opencode"]) {
      add(__ocpHost.appData && join(__ocpHost.appData, name));
      add(__ocpHost.localAppData && join(__ocpHost.localAppData, name));
    }
    add(__ocpHost.localAppData && join(__ocpHost.localAppData, "@opencode-aidesktop-updater"));
    add(__ocpHost.localAppData && join(__ocpHost.localAppData, "opencode-updater"));
    add(__ocpHost.localAppData && join(__ocpHost.localAppData, "Programs", "opencode"));
    add(__ocpHost.localAppData && join(__ocpHost.localAppData, "Programs", "OpenCode"));
    add(__ocpHost.temp && join(__ocpHost.temp, "opencode"));
    for (const suffix of [
      [".opencode"],
      [".config", "opencode"],
      [".cache", "opencode"],
      [".local", "share", "opencode"],
      [".local", "state", "opencode"],
    ]) {
      add(__ocpHost.home && join(__ocpHost.home, ...suffix));
    }
    return [...new Set(out)];
  };

  const __ocpReadJson = (file) => {
    try {
      return JSON.parse(readFileSync(file, "utf8"));
    } catch {
      return undefined;
    }
  };
  const __ocpWriteJson = (file, value) => {
    try {
      writeFileSync(file, JSON.stringify(value, null, 2), "utf8");
    } catch {}
  };

  let __ocpBaseline = __ocpReadJson(__ocpBaselineFile);
  if (!__ocpBaseline || !Array.isArray(__ocpBaseline.existed)) {
    __ocpBaseline = {
      version: 1,
      firstRun: new Date().toISOString(),
      existed: __ocpCandidates().filter((path) => {
        try {
          return existsSync(path);
        } catch {
          return false;
        }
      }),
    };
    __ocpWriteJson(__ocpBaselineFile, __ocpBaseline);
  }

  const __ocpRegistryKey = "HKCU\\Software\\Classes\\opencode";
  const __ocpRegistryPointsAtUs = () => {
    if (process.platform !== "win32") return false;
    try {
      const result = spawnSync("reg.exe", ["query", __ocpRegistryKey, "/ve"], { encoding: "utf8", windowsHide: true });
      return result.status === 0 && typeof result.stdout === "string" && result.stdout.includes(process.execPath);
    } catch {
      return false;
    }
  };

  // paths created by *this* stick that are still sitting on the host computer
  const __ocpResidue = () => {
    const known = new Set(__ocpBaseline.existed || []);
    const found = [];
    for (const path of __ocpCandidates()) {
      if (known.has(path)) continue;
      try {
        if (existsSync(path)) found.push(path);
      } catch {}
    }
    return found;
  };

  // runs at startup: delete anything a previous (crashed / yanked) session
  // recorded as its own residue, never anything the host had before.
  const __ocpCleanupRecordedResidue = () => {
    const record = __ocpReadJson(__ocpResidueFile);
    const paths = Array.isArray(record?.paths) ? record.paths : [];
    let removed = 0;
    for (const path of paths) {
      const known = new Set(__ocpBaseline.existed || []);
      if (known.has(path)) continue;
      try {
        rmSync(path, { recursive: true, force: true });
        removed++;
      } catch {}
    }
    if (record?.registry && __ocpRegistryPointsAtUs()) {
      try {
        spawnSync("reg.exe", ["delete", __ocpRegistryKey, "/f"], { windowsHide: true });
        removed++;
      } catch {}
    }
    if (removed > 0 || record) __ocpWriteJson(__ocpResidueFile, { version: 1, paths: [], registry: false });
  };

  const __ocpRecordResidue = () => {
    const paths = __ocpResidue();
    const registry = __ocpRegistryPointsAtUs();
    if (paths.length === 0 && !registry) {
      __ocpWriteJson(__ocpResidueFile, { version: 1, paths: [], registry: false });
      return;
    }
    __ocpWriteJson(__ocpResidueFile, { version: 1, checkedAt: new Date().toISOString(), paths, registry });
    // best effort: get rid of it right away too
    for (const path of paths) {
      try {
        rmSync(path, { recursive: true, force: true });
      } catch {}
    }
    if (registry && __ocpRegistryPointsAtUs()) {
      try {
        spawnSync("reg.exe", ["delete", __ocpRegistryKey, "/f"], { windowsHide: true });
      } catch {}
    }
  };

  try {
    __ocpCleanupRecordedResidue();
  } catch {}

  try {
    const timer = setInterval(() => {
      try {
        __ocpRecordResidue();
      } catch {}
    }, 30_000);
    timer.unref?.();
    app.once("will-quit", () => {
      clearInterval(timer);
      try {
        __ocpRecordResidue();
      } catch {}
    });
    process.on("exit", () => {
      try {
        __ocpRecordResidue();
      } catch {}
    });
  } catch {}

  // surface portable mode in the log file
  try {
    const log = (...parts) => process.stdout?.write?.(`[opencode-portable] ${parts.join(" ")}\n`);
    log(`root=${__ocpRoot} userData=${__ocpDirs.userdata} xdg=${__ocpData}`);
  } catch {}
}
/* === end of OpenCode Portable bootstrap === */
