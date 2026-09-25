import base64, errno, fcntl, hashlib, json, os, re, secrets, stat, sys, tempfile

SPILL_BYTES = 4 << 20
SNAP_PREFIX = "/tmp/mecatl-boat-snap-"
STAGE_PREFIX = "/tmp/mecatl-boat-stage-"


def _write(text):
    sys.stdout.write(text)
    sys.stdout.flush()
    raise SystemExit(0)


def emit(value):
    text = json.dumps(value, separators=(",", ":"))
    if len(text) > SPILL_BYTES:
        snap = SNAP_PREFIX + secrets.token_hex(12)
        with open(snap, "wb") as f:
            f.write(text.encode())
        _write(json.dumps({"ok": True, "spill": snap, "size": len(text.encode())}))
    _write(text)


class Fail(Exception):
    def __init__(self, code, detail=""):
        super().__init__(code)
        self.code, self.detail = code, detail


def fail(code, detail=""):
    raise Fail(code, detail)


def report(f):
    out = {"ok": False, "code": f.code}
    if f.detail:
        out["detail"] = str(f.detail)[:300]
    _write(json.dumps(out))


def b64(data):
    return base64.b64encode(data).decode()


def version(data):
    return hashlib.sha256(data).hexdigest()


def load_request():
    req = json.loads(base64.b64decode(sys.argv[1]))
    if "stage" not in req:
        return req
    buf = bytearray()
    for part in req["stage"]:
        if not isinstance(part, str) or not part.startswith(STAGE_PREFIX) or "/" in part[len(STAGE_PREFIX):]:
            fail("invalid", "bad stage path")
        with open(part, "rb") as f:
            buf += f.read()
        os.unlink(part)
    return json.loads(bytes(buf))


try:
    req = load_request()
except Fail as f:
    report(f)

root = os.path.realpath(os.getcwd())
lock_dir = req.get("lock_dir") or "/tmp"


def within(p):
    return p == root or p.startswith(root.rstrip("/") + "/")


def parts_of(rel):
    if not isinstance(rel, str) or "\x00" in rel or rel.startswith("/"):
        fail("invalid", "path must be relative")
    parts = [x for x in rel.split("/") if x not in ("", ".")]
    if ".." in parts:
        fail("escape")
    return parts


def resolved(rel):
    """Follow every symlink; the result must stay inside the workspace."""
    parts = parts_of(rel)
    p = os.path.realpath(os.path.join(root, *parts)) if parts else root
    if not within(p):
        fail("escape")
    return p


def leaf(rel):
    """Resolve the parent but keep the final component itself, so mutations
    act on a symlink rather than the file it points at."""
    parts = parts_of(rel)
    if not parts:
        fail("invalid", "operation requires a file path")
    parent = os.path.realpath(os.path.join(root, *parts[:-1])) if len(parts) > 1 else root
    if not within(parent):
        fail("escape")
    return os.path.join(parent, parts[-1])


def refuse_existing_leaf(dst):
    if os.path.islink(dst) and not within(os.path.realpath(dst)):
        fail("escape")
    if os.path.lexists(dst):
        fail("exists")


def locked():
    os.makedirs(lock_dir, exist_ok=True)
    key = hashlib.sha256(root.encode()).hexdigest()
    f = open(os.path.join(lock_dir, "mecatl-boat-" + key + ".lock"), "a+b")
    fcntl.flock(f.fileno(), fcntl.LOCK_EX)
    return f


def info_of(st, name):
    return {"name": name, "size": st.st_size, "perm": stat.S_IMODE(st.st_mode),
            "mod_time_ns": st.st_mtime_ns, "is_dir": stat.S_ISDIR(st.st_mode),
            "is_symlink": stat.S_ISLNK(st.st_mode)}


def write_temp(directory, data, mode):
    fd, tmp = tempfile.mkstemp(prefix=".mecatl-write-", dir=directory)
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.chmod(tmp, mode)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
    return tmp


def create_only(dst, data):
    """Publish complete content at dst or fail if anything already exists
    there; a reader never observes a partially written new file."""
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    tmp = write_temp(os.path.dirname(dst), data, 0o644)
    try:
        try:
            os.link(tmp, dst)
        except OSError as e:
            if e.errno not in (errno.EPERM, errno.EOPNOTSUPP, errno.EXDEV, errno.EMLINK):
                raise
            fd = os.open(dst, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
            with os.fdopen(fd, "wb") as f:
                f.write(data)
    finally:
        os.unlink(tmp)


def kind_of(mode):
    if stat.S_ISDIR(mode):
        return "d"
    if stat.S_ISLNK(mode):
        return "l"
    if stat.S_ISREG(mode):
        return "f"
    return "o"


def target_kind(path):
    """For a symlink: the kind it resolves to inside the workspace, or ""."""
    try:
        target = os.path.realpath(path)
        if not within(target):
            return ""
        return kind_of(os.stat(target).st_mode)
    except OSError:
        return ""


def op_read(req):
    p = resolved(req.get("path", ""))
    limit = int(req.get("inline_limit", 0))
    max_bytes = int(req.get("max_bytes", 0))
    with open(p, "rb") as f:
        st = os.fstat(f.fileno())
        if not stat.S_ISREG(st.st_mode):
            fail("unsupported", "not a regular file")
        if max_bytes and st.st_size > max_bytes:
            fail("too_large", str(st.st_size))
        data = f.read()
    if len(data) <= limit:
        emit({"ok": True, "content": b64(data), "version": version(data)})
    snap = SNAP_PREFIX + secrets.token_hex(12)
    with open(snap, "wb") as f:
        f.write(data)
    emit({"ok": True, "snapshot": snap, "size": len(data), "version": version(data)})


def checked_snapshot(req):
    snap = req.get("snapshot", "")
    if not isinstance(snap, str) or not snap.startswith(SNAP_PREFIX) or "/" in snap[len(SNAP_PREFIX):]:
        fail("invalid", "bad snapshot path")
    return snap


def op_range(req):
    snap = checked_snapshot(req)
    offset, length = int(req.get("offset", 0)), int(req.get("length", 0))
    with open(snap, "rb") as f:
        f.seek(offset)
        chunk = f.read(length)
        size = os.fstat(f.fileno()).st_size
    if req.get("drop") and offset + len(chunk) >= size:
        os.unlink(snap)
    # Never spilled: a range is already bounded by the caller.
    _write(json.dumps({"ok": True, "content": b64(chunk)}))


def op_drop(req):
    try:
        os.unlink(checked_snapshot(req))
    except FileNotFoundError:
        pass
    emit({"ok": True})


def op_stat(req):
    parts = parts_of(req.get("path", ""))
    p = resolved(req.get("path", ""))
    emit({"ok": True, "info": info_of(os.stat(p), parts[-1] if parts else ".")})


def op_create(req):
    dst = leaf(req.get("path", ""))
    data = base64.b64decode(req.get("content", ""))
    with locked():
        refuse_existing_leaf(dst)
        create_only(dst, data)
    emit({"ok": True, "version": version(data)})


def op_replace(req):
    p = resolved(req.get("path", ""))
    if p == root:
        fail("invalid", "operation requires a file path")
    data = base64.b64decode(req.get("content", ""))
    with locked():
        if not os.path.lexists(p):
            fail("not_found")
        if not os.path.isfile(p):
            fail("unsupported", "not a regular file")
        with open(p, "rb") as f:
            current = f.read()
        if not req.get("expected_valid") or version(current) != req.get("expected", ""):
            fail("version_mismatch")
        tmp = write_temp(os.path.dirname(p), data, stat.S_IMODE(os.stat(p).st_mode))
        try:
            os.replace(tmp, p)
        finally:
            try:
                os.unlink(tmp)
            except FileNotFoundError:
                pass
    emit({"ok": True, "version": version(data)})


def tree_entry(rel, st):
    return {"p": rel, "t": kind_of(st.st_mode), "s": st.st_size, "m": stat.S_IMODE(st.st_mode), "n": st.st_mtime_ns}


def walk_tree(abs_dir, rel_dir, entries, max_entries, depth):
    """Pre-order, name-sorted, never following symlinks: the same visit order
    as Go's filepath.WalkDir. depth bounds how many levels are listed (0 means
    unbounded)."""
    try:
        children = sorted(os.scandir(abs_dir), key=lambda e: e.name)
    except OSError:
        return
    for e in children:
        try:
            e.name.encode("utf-8")
            st = e.stat(follow_symlinks=False)
        except (UnicodeEncodeError, OSError):
            continue
        entry = tree_entry(e.name if not rel_dir else rel_dir + "/" + e.name, st)
        if entry["t"] == "l":
            entry["k"] = target_kind(e.path)
        entries.append(entry)
        if len(entries) > max_entries:
            fail("too_many", str(max_entries))
        if entry["t"] == "d" and depth != 1:
            walk_tree(e.path, entry["p"], entries, max_entries, depth - 1 if depth else 0)


def op_tree(req):
    base = "/".join(parts_of(req.get("base", "")))
    start = resolved(base)
    entries = []
    if base:
        try:
            bst = os.stat(start)
        except OSError:
            emit({"ok": True, "tree": entries})
        entries.append(tree_entry(base, bst))
        if not stat.S_ISDIR(bst.st_mode):
            emit({"ok": True, "tree": entries})
    walk_tree(start, base, entries, int(req.get("max_entries", 200000)), int(req.get("max_depth", 0)))
    emit({"ok": True, "tree": entries})


def grep_file(rel, rx, literal, matches, max_matches):
    try:
        with open(resolved(rel), "rb") as f:
            data = f.read()
    except (OSError, Fail):
        return
    if b"\x00" in data or (literal and literal not in data):
        return
    for n, line in enumerate(data.split(b"\n"), 1):
        text = line.decode("utf-8", "replace")
        if rx.search(text):
            matches.append({"p": rel, "l": n, "x": text})
            if len(matches) >= max_matches:
                emit({"ok": True, "matches": matches})


def op_grep(req):
    rx = re.compile(req.get("pattern", ""), re.ASCII)
    literal = base64.b64decode(req.get("literal", ""))
    max_matches = int(req.get("max_matches", 201))
    max_files = int(req.get("max_files", 10000))
    max_bytes = int(req.get("max_bytes", 64 << 20))
    matches, files, total = [], 0, 0
    for rel in req.get("files", []):
        try:
            st = os.lstat(resolved(rel))
        except (OSError, Fail):
            continue
        if not stat.S_ISREG(st.st_mode):
            continue
        files += 1
        if files > max_files or st.st_size > max_bytes - total:
            fail("budget")
        total += st.st_size
        grep_file(rel, rx, literal, matches, max_matches)
    emit({"ok": True, "matches": matches})


def op_readdir(req):
    p = resolved(req.get("path", "."))
    if not os.path.exists(p):
        fail("not_found")
    if not os.path.isdir(p):
        fail("unsupported", "not a directory")
    entries = []
    for child in sorted(os.scandir(p), key=lambda e: e.name):
        try:
            entries.append(info_of(child.stat(follow_symlinks=False), child.name))
        except OSError:
            continue
    emit({"ok": True, "entries": entries})


def op_remove(req):
    dst = leaf(req.get("path", ""))
    with locked():
        st = os.lstat(dst)
        if stat.S_ISDIR(st.st_mode):
            os.rmdir(dst)
        else:
            os.unlink(dst)
    emit({"ok": True})


def op_rename(req):
    src = leaf(req.get("old_path", ""))
    dst = leaf(req.get("new_path", ""))
    with locked():
        if not os.path.lexists(src):
            fail("not_found")
        refuse_existing_leaf(dst)
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        os.rename(src, dst)
    emit({"ok": True})


def op_copy(req):
    src = resolved(req.get("old_path", ""))
    dst = leaf(req.get("new_path", ""))
    with locked():
        if not os.path.lexists(src):
            fail("not_found")
        if not os.path.isfile(src):
            fail("unsupported", "copy source is not a regular file")
        refuse_existing_leaf(dst)
        with open(src, "rb") as f:
            data = f.read()
        create_only(dst, data)
    emit({"ok": True, "version": version(data)})


def op_resolve(req):
    emit({"ok": True, "target": resolved(req.get("path", ".")), "root": root})


OPS = {"read": op_read, "range": op_range, "drop": op_drop, "stat": op_stat, "create": op_create,
       "replace": op_replace, "tree": op_tree, "grep": op_grep, "readdir": op_readdir,
       "remove": op_remove, "rename": op_rename, "copy": op_copy, "resolve": op_resolve}


def main(req):
    handler = OPS.get(req.get("op", ""))
    if handler is None:
        fail("invalid", "unknown operation")
    handler(req)


try:
    main(req)
except Fail as f:
    report(f)
except FileNotFoundError:
    report(Fail("not_found"))
except FileExistsError:
    report(Fail("exists"))
except (IsADirectoryError, NotADirectoryError):
    report(Fail("unsupported", "wrong file type for this operation"))
except re.error as e:
    report(Fail("invalid", "pattern: %s" % e))
except OSError as e:
    if e.errno in (errno.ENOTEMPTY, errno.EEXIST):
        report(Fail("not_empty"))
    report(Fail("io", os.strerror(e.errno) if e.errno else str(e)))
