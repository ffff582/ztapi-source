import Darwin
import Foundation

enum Failure: String, Error {
    case request = "PROJECT_REQUEST_INVALID"
    case root = "PROJECT_ROOT_REJECTED"
    case path = "PROJECT_PATH_REJECTED"
    case collision = "PROJECT_CASE_COLLISION"
    case limit = "PROJECT_LIMIT"
    case changed = "PROJECT_CHANGED"
    case io = "PROJECT_IO_FAILED"
    case uncertain = "PROJECT_WRITEBACK_UNCERTAIN"
}

let maxFile = 2 * 1024 * 1024
let maxTotal = 32 * 1024 * 1024
let maxFiles = 3000
let excluded = try! NSRegularExpression(pattern:
    #"^(?:\.env(?:\..*)?|\.envrc|\.direnv|\.git|\.ssh|\.aws|\.azure|\.docker|\.kube|\.config|\.codex|\.runtime-tmp|\.ztapi-write-.*|node_modules|\.venv|venv|dist|target|\.npmrc|\.netrc|\.pypirc|auth\.json|credentials\.json|id_(?:rsa|ecdsa|dsa|ed25519).*|(?:con|nul|prn|aux|com[1-9]|lpt[1-9])(?:\..*)?|.*\.(?:pem|key|p12|pfx|jks|keystore))$"#,
    options: .caseInsensitive)

struct ProjectFile: Codable {
    let path: String
    let data: String
    let identity: String
}
struct Snapshot: Encodable {
    let root: String
    let rootIdentity: String
    let files: [ProjectFile]
}
struct SnapshotRequest: Decodable { let root: String }
struct CommitRequest: Decodable {
    let root: String
    let rootIdentity: String
    let before: ProjectFile
    let after: String
}
struct CommitResult: Encodable { let status: String; let file: ProjectFile }
struct ErrorResult: Encodable { let code: String }

func validSegment(_ name: String) -> Bool {
    guard !name.isEmpty, name != ".", name != "..", !name.hasSuffix("."), !name.hasSuffix(" "),
          !name.utf8.contains(where: { $0 < 32 || $0 == 58 || $0 == 92 || $0 == 47 }) else { return false }
    return excluded.firstMatch(in: name, range: NSRange(name.startIndex..., in: name)) == nil
}

func isType(_ info: stat, _ kind: mode_t) -> Bool {
    info.st_mode & S_IFMT == kind
}

func identity(_ info: stat) -> String {
    "\(info.st_dev):\(info.st_ino):\(info.st_birthtimespec.tv_sec):\(info.st_birthtimespec.tv_nsec)"
}

func sameObject(_ left: stat, _ right: stat) -> Bool {
    left.st_dev == right.st_dev && left.st_ino == right.st_ino
}

func sameState(_ left: stat, _ right: stat) -> Bool {
    identity(left) == identity(right) && left.st_size == right.st_size &&
    left.st_ctimespec.tv_sec == right.st_ctimespec.tv_sec && left.st_ctimespec.tv_nsec == right.st_ctimespec.tv_nsec &&
    left.st_mtimespec.tv_sec == right.st_mtimespec.tv_sec && left.st_mtimespec.tv_nsec == right.st_mtimespec.tv_nsec &&
    left.st_mode == right.st_mode && left.st_nlink == right.st_nlink
}

func statFD(_ fd: Int32) throws -> stat {
    var info = stat()
    guard fstat(fd, &info) == 0 else { throw Failure.io }
    return info
}

func statName(_ fd: Int32, _ name: String) throws -> stat {
    var info = stat()
    guard fstatat(fd, name, &info, AT_SYMLINK_NOFOLLOW) == 0 else { throw Failure.changed }
    return info
}

func openDirs(_ start: Int32, _ parts: [String], _ failure: Failure) throws -> Int32 {
    var current = dup(start)
    guard current >= 0 else { throw Failure.io }
    for part in parts {
        let next = openat(current, part, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
        close(current)
        guard next >= 0 else { throw failure }
        current = next
    }
    return current
}

func openRoot(_ root: String) throws -> Int32 {
    let home = NSHomeDirectory().precomposedStringWithCanonicalMapping.lowercased()
    let folded = root.precomposedStringWithCanonicalMapping.lowercased()
    let parts = root.components(separatedBy: "/")
    guard root.hasPrefix("/"), root.utf8.count < PATH_MAX, parts.count >= 3,
          !parts.dropFirst().contains(where: { $0.isEmpty || $0 == "." || $0 == ".." || $0.utf8.contains(0) }),
          folded != home, folded != home + "/library", !folded.hasPrefix(home + "/library/"),
          !["/system", "/library", "/usr", "/bin", "/sbin", "/dev", "/private/etc", "/private/var/db"]
              .contains(where: { folded == $0 || folded.hasPrefix($0 + "/") }) else { throw Failure.root }
    let fd = open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC)
    guard fd >= 0 else { throw Failure.root }
    defer { close(fd) }
    return try openDirs(fd, Array(parts.dropFirst()), .root)
}

func readChecked(_ fd: Int32, _ baseline: stat) throws -> Data {
    guard isType(baseline, S_IFREG), baseline.st_nlink == 1, baseline.st_size >= 0 else { throw Failure.path }
    guard baseline.st_size <= maxFile else { throw Failure.limit }
    guard lseek(fd, 0, SEEK_SET) == 0 else { throw Failure.io }
    var data = Data()
    let size = 64 * 1024
    var buffer = [UInt8](repeating: 0, count: size)
    while true {
        let count = buffer.withUnsafeMutableBytes { read(fd, $0.baseAddress, size) }
        if count == 0 { break }
        if count < 0 {
            if errno == EINTR { continue }
            throw Failure.io
        }
        guard data.count + count <= maxFile, data.count + count <= baseline.st_size else { throw Failure.changed }
        data.append(contentsOf: buffer.prefix(count))
    }
    guard data.count == baseline.st_size, sameState(baseline, try statFD(fd)) else { throw Failure.changed }
    return data
}

final class Scanner {
    var files: [ProjectFile] = []
    var total = 0
    var entries = 0

    func scan(_ fd: Int32, _ prefix: String, _ device: dev_t, _ depth: Int) throws {
        guard depth <= 32 else { throw Failure.limit }
        let duplicate = dup(fd)
        guard duplicate >= 0 else { throw Failure.io }
        guard let dir = fdopendir(duplicate) else { close(duplicate); throw Failure.io }
        defer { closedir(dir) }
        var names = Set<String>()
        while true {
            errno = 0
            guard let entry = readdir(dir) else {
                if errno != 0 { throw Failure.io }
                break
            }
            let name = withUnsafePointer(to: &entry.pointee.d_name) {
                $0.withMemoryRebound(to: CChar.self, capacity: Int(MAXNAMLEN) + 1) { String(validatingUTF8: $0) }
            }
            guard let name = name, name != ".", name != ".." else { continue }
            entries += 1
            guard entries <= 12000 else { throw Failure.limit }
            guard validSegment(name) else { continue }
            let folded = name.precomposedStringWithCanonicalMapping.lowercased()
            guard names.insert(folded).inserted else { throw Failure.collision }
            let relative = prefix + name
            guard relative.utf8.count <= 500 else { throw Failure.limit }
            let observed = try statName(fd, name)
            guard observed.st_dev == device else { continue }
            if isType(observed, S_IFDIR) {
                let child = try openDirs(fd, [name], .changed)
                defer { close(child) }
                guard identity(try statFD(child)) == identity(observed) else { throw Failure.changed }
                try scan(child, relative + "/", device, depth + 1)
                guard identity(try statName(fd, name)) == identity(observed) else { throw Failure.changed }
            } else if isType(observed, S_IFREG), observed.st_nlink == 1 {
                guard files.count < maxFiles, observed.st_size >= 0, observed.st_size <= maxFile,
                      total + Int(observed.st_size) <= maxTotal else { throw Failure.limit }
                let child = openat(fd, name, O_RDONLY | O_NONBLOCK | O_NOFOLLOW | O_CLOEXEC)
                guard child >= 0 else { throw Failure.changed }
                defer { close(child) }
                guard sameState(observed, try statFD(child)) else { throw Failure.changed }
                let bytes = try readChecked(child, observed)
                guard sameState(observed, try statName(fd, name)) else { throw Failure.changed }
                total += bytes.count
                files.append(ProjectFile(path: relative, data: bytes.base64EncodedString(), identity: identity(observed)))
            }
        }
    }
}

func snapshot(_ root: String) throws -> Snapshot {
    let fd = try openRoot(root)
    defer { close(fd) }
    let info = try statFD(fd)
    let scanner = Scanner()
    try scanner.scan(fd, "", info.st_dev, 0)
    let named = try openRoot(root)
    defer { close(named) }
    guard identity(try statFD(named)) == identity(info) else { throw Failure.changed }
    return Snapshot(root: root, rootIdentity: identity(info), files: scanner.files.sorted { $0.path < $1.path })
}

func writeAll(_ fd: Int32, _ data: Data) throws {
    try data.withUnsafeBytes { bytes in
        guard let base = bytes.baseAddress else { return }
        var offset = 0
        while offset < bytes.count {
            let count = write(fd, base.advanced(by: offset), bytes.count - offset)
            if count < 0 && errno == EINTR { continue }
            guard count > 0 else { throw Failure.io }
            offset += count
        }
    }
}

func commit(_ request: CommitRequest) throws -> CommitResult {
    let parts = request.before.path.components(separatedBy: "/")
    guard request.before.path.utf8.count <= 500, parts.count <= 32, parts.allSatisfy(validSegment),
          let name = parts.last, let before = Data(base64Encoded: request.before.data),
          let after = Data(base64Encoded: request.after),
          before.count <= maxFile, after.count <= maxFile else { throw Failure.request }
    let root = try openRoot(request.root)
    defer { close(root) }
    let rootInfo = try statFD(root)
    guard identity(rootInfo) == request.rootIdentity else { throw Failure.changed }
    let parents = Array(parts.dropLast())
    let dir = try openDirs(root, parents, .changed)
    defer { close(dir) }
    let dirID = identity(try statFD(dir))
    let initial = try statName(dir, name)
    guard isType(initial, S_IFREG), initial.st_nlink == 1, initial.st_dev == rootInfo.st_dev,
          initial.st_uid == geteuid(), initial.st_mode & 0o7000 == 0,
          identity(initial) == request.before.identity else { throw Failure.changed }
    let source = openat(dir, name, O_RDONLY | O_NONBLOCK | O_NOFOLLOW | O_CLOEXEC)
    guard source >= 0 else { throw Failure.changed }
    defer { close(source) }
    guard flock(source, LOCK_EX | LOCK_NB) == 0, sameState(initial, try statFD(source)),
          try readChecked(source, initial) == before else { throw Failure.changed }

    let staging = ".ztapi-write-" + UUID().uuidString.lowercased()
    let temp = openat(dir, staging, O_RDWR | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
    guard temp >= 0 else { throw Failure.io }
    defer { close(temp) }
    var removeTemp = true
    let stagedInfo = try statFD(temp)
    defer {
        if removeTemp, let current = try? statName(dir, staging), sameObject(current, stagedInfo) {
            _ = unlinkat(dir, staging, 0)
        }
    }
    try writeAll(temp, after)
    guard fcopyfile(source, temp, nil, copyfile_flags_t(COPYFILE_METADATA)) == 0, fsync(temp) == 0 else { throw Failure.io }

    func verifyAnchor() throws {
        let named = try openRoot(request.root)
        defer { close(named) }
        guard identity(try statFD(named)) == request.rootIdentity else { throw Failure.changed }
        let current = try openDirs(named, parents, .changed)
        defer { close(current) }
        guard identity(try statFD(current)) == dirID else { throw Failure.changed }
    }
    try verifyAnchor()
    guard sameState(initial, try statName(dir, name)), try readChecked(source, initial) == before,
          sameObject(try statName(dir, staging), stagedInfo) else { throw Failure.changed }
    // Swap retains the displaced entry so a last-moment concurrent replacement is never discarded.
    guard renameatx_np(dir, staging, dir, name, UInt32(RENAME_SWAP)) == 0 else { throw Failure.io }
    removeTemp = false
    let displaced = try? statName(dir, staging)
    do {
        try verifyAnchor()
        guard let displaced = displaced, identity(displaced) == identity(initial),
              try readChecked(source, try statFD(source)) == before,
              sameObject(try statName(dir, name), stagedInfo) else { throw Failure.changed }
    } catch {
        guard let displaced = displaced,
              let current = try? statName(dir, name), sameObject(current, stagedInfo),
              let backup = try? statName(dir, staging), identity(backup) == identity(displaced),
              renameatx_np(dir, staging, dir, name, UInt32(RENAME_SWAP)) == 0 else { throw Failure.uncertain }
        removeTemp = true
        throw Failure.changed
    }
    let committed = try statName(dir, name)
    guard sameObject(committed, stagedInfo) else { throw Failure.uncertain }
    guard fsync(dir) == 0, let backup = try? statName(dir, staging),
          identity(backup) == identity(initial), unlinkat(dir, staging, 0) == 0 else { throw Failure.uncertain }
    return CommitResult(status: "committed", file: ProjectFile(path: request.before.path,
        data: after.base64EncodedString(), identity: identity(committed)))
}

func emit<T: Encodable>(_ value: T) throws {
    FileHandle.standardOutput.write(try JSONEncoder().encode(value))
}

do {
    let limit = 6 * 1024 * 1024
    let data = FileHandle.standardInput.readData(ofLength: limit + 1)
    guard CommandLine.arguments.count == 2, data.count <= limit else { throw Failure.request }
    let decoder = JSONDecoder()
    switch CommandLine.arguments[1] {
    case "snapshot": try emit(snapshot(decoder.decode(SnapshotRequest.self, from: data).root))
    case "commit-file": try emit(commit(decoder.decode(CommitRequest.self, from: data)))
    default: throw Failure.request
    }
} catch {
    try? emit(ErrorResult(code: (error as? Failure ?? .request).rawValue))
    exit(1)
}
