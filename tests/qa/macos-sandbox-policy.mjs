import { posix } from 'node:path';

const base = `(version 1)
(deny default)
(allow process-exec (literal (param "PROGRAM")))
(allow file-read* (literal (param "PROGRAM")))
(allow file-read* (subpath "/usr/lib") (subpath "/System/Library"))
(allow file-read* file-map-executable
    (subpath "/System/Library/dyld")
    (subpath "/System/Cryptexes/OS/System/Library/dyld")
    (subpath "/System/Volumes/Preboot/Cryptexes/OS/System/Library/dyld"))
(allow file-read* file-write* (subpath (param "WORK")))
`;

function ordinaryFixturePath(path) {
  return typeof path === 'string' && !/[\r\n\x00]/.test(path) &&
    posix.normalize(path) === path && !path.endsWith('/') &&
    /^\/private\/(tmp|var\/folders)\/.+/.test(path);
}

// QA-only policy for a freshly created fixture. It is not an application launcher.
export function seatbeltArgs({ workspace, program, args, brokerPort = null, loaderCache = null }) {
  if (!ordinaryFixturePath(workspace) || !ordinaryFixturePath(program) ||
      workspace === program || program.startsWith(workspace + '/') ||
      !Array.isArray(args) || args.some(value => typeof value !== 'string' || value.includes('\0')) ||
      (brokerPort !== null && (!Number.isInteger(brokerPort) || brokerPort < 1 || brokerPort > 65535)) ||
      (loaderCache !== null && (typeof loaderCache !== 'string' || posix.normalize(loaderCache) !== loaderCache ||
          !/^\/System\/(Library\/dyld|(?:Cryptexes|Volumes\/Preboot)\/[A-Za-z0-9_./-]+\/System\/Library\/dyld)$/.test(loaderCache)))) {
    throw new Error('Invalid isolated fixture');
  }
  const params = ['-D', `WORK=${workspace}`, '-D', `PROGRAM=${program}`];
  let policy = base;
  if (loaderCache !== null) {
    params.push('-D', `DYLD_DIR=${loaderCache}`);
    policy += '(allow file-read* file-map-executable (subpath (param "DYLD_DIR")))\n';
  }
  if (brokerPort !== null) {
    params.push('-D', `BROKER=localhost:${brokerPort}`);
    policy += '(allow network-outbound (remote ip (param "BROKER")))\n';
  }
  return [...params, '-p', policy, program, ...args];
}
