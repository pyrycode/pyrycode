/** Checks this executable; never selects a replacement runtime. @param {string} version */
export function assertRuntime(version = process.versions.node) {
  const match = /^(\d+)\.(\d+)\.(\d+)$/.exec(version);
  if (!match || Number(match[1]) !== 24 || Number(match[2]) < 15) {
    throw new Error(`hosted-app requires Node >=24.15.0 <25.0.0; actual ${version}`);
  }
}
assertRuntime();
