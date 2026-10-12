"""Private controller and retained pinned MemSearch worker; fd 3 channel, fd 4 lock."""
import asyncio
import fcntl
import json
import os
import select
import signal
import socket
import stat
import subprocess
import time
import uuid

os.umask(0o077)
channel = socket.socket(fileno=3)
lock_fd = 4

def validate_tree(path):
    for base, dirs, files in os.walk(path, followlinks=False):
        for name in [base] + [os.path.join(base, n) for n in dirs + files]:
            s = os.lstat(name)
            directory = stat.S_ISDIR(s.st_mode)
            if s.st_uid != os.geteuid() or stat.S_IMODE(s.st_mode) != (0o700 if directory else 0o600):
                raise ValueError("unsafe")
            if not directory and (not stat.S_ISREG(s.st_mode) or s.st_nlink != 1):
                raise ValueError("unsafe")

def receive(sock, size):
    try:
        return sock.recv(size)
    except OSError:
        return b""

def worker(control, request):
    # Every backend launch retains exclusion, even if the worker dies first.
    launch = subprocess.Popen
    def owned_launch(*args, **kwargs):
        kwargs["pass_fds"] = tuple(set(kwargs.get("pass_fds", ())) | {lock_fd})
        return launch(*args, **kwargs)
    subprocess.Popen = owned_launch
    memory = None
    try:
        store = request["store"]
        validate_tree(store)
        models = os.path.join(store, "models")
        os.makedirs(models, mode=0o700, exist_ok=True)
        model = request["model"]
        compatibility = {"format": request["versions"]["milvus-lite"],
                         "memsearch": request["versions"]["memsearch"],
                         "provider": request["provider"], "model": model}
        metadata = os.path.join(store, "compatibility.json")
        existing = None
        if os.path.exists(metadata):
            with open(metadata) as f:
                existing = json.load(f)
            if any(existing.get(k) != v for k, v in compatibility.items()):
                raise ValueError("incompatible")
        database = os.path.join(store, "milvus.db")
        if os.path.exists(database) and existing is None:
            raise ValueError("unknown database")
        # A private flat download avoids Hugging Face's symlink snapshot cache.
        model = request["model"]
        if request["provider"] == "local":
            from huggingface_hub import snapshot_download
            from sentence_transformers import SentenceTransformer
            import sentence_transformers
            downloaded = snapshot_download(model if "/" in model else "sentence-transformers/" + model,
                                           local_dir=os.path.join(models, "selected"))
            def selected_transformer(_model, **kwargs):
                kwargs["trust_remote_code"] = False
                return SentenceTransformer(downloaded, **kwargs)
            sentence_transformers.SentenceTransformer = selected_transformer
        from memsearch import MemSearch
        memory = MemSearch(paths=[], embedding_provider=request["provider"], embedding_model=model,
                           embedding_api_key=request["credential"] or None,
                           embedding_base_url=request.get("endpoint"), milvus_uri=database)
        vectors = asyncio.run(memory._embedder.embed(["pyry managed memory health probe"]))
        dimension = memory._embedder.dimension
        if len(vectors) != 1 or len(vectors[0]) != dimension or dimension <= 0:
            raise ValueError("dimension")
        compatibility["dimension"] = dimension
        if existing is not None and compatibility != existing:
            raise ValueError("incompatible")
        if existing is None:
            temporary = metadata + "." + uuid.uuid4().hex
            with open(temporary, "x") as f:
                json.dump(compatibility, f)
                f.flush()
                os.fsync(f.fileno())
            os.replace(temporary, metadata)
        # The pinned fresh constructor is lazy. Connect/create without indexing.
        memory.store._ensure_collection_for_write()
        memory.store._connected_client().list_collections()
        if globals().get("PYRY_TEST_EMPTY", False):
            assert int(memory.store.count()) == 0
        control.sendall(b"ready\n")
        control.recv(1)
    except BaseException:
        control.sendall(b"failed\n")
    finally:
        try:
            if memory is not None:
                memory.close()
            control.sendall(b"closed\n")
        except BaseException:
            pass
        control.close()
        os._exit(0)

pid = None
try:
    raw = bytearray()
    while not raw.endswith(b"\n"):
        chunk = channel.recv(4096)
        if not chunk or len(raw) + len(chunk) > 65536:
            raise ValueError("request")
        raw.extend(chunk)
    request = json.loads(raw)
    parent, child = socket.socketpair()
    pid = os.fork()
    if pid == 0:
        parent.close()
        channel.close()
        os.setpgid(0, 0)
        worker(child, request)
    child.close()
    os.setpgid(pid, pid)
    finished = False
    while True:
        readable, _, _ = select.select([channel, parent], [], [])
        if channel in readable:
            break
        if parent in readable:
            status = receive(parent, 64)
            if status == b"ready\n":
                channel.sendall(status)
            else:
                break
    try:
        parent.sendall(b"s")
    except OSError:
        pass
    # Keep the direct child unreaped until after group signalling. Its PID cannot
    # be reused to identify an unrelated process group during this interval.
    end = time.monotonic() + 2
    while time.monotonic() < end:
        if select.select([parent], [], [], 0.05)[0]:
            status = receive(parent, 64)
            if b"closed" in status:
                finished = True
                break
            if not status:
                break
    for sig, delay in [(signal.SIGTERM, 0.1 if finished else 2), (signal.SIGKILL, 0.1)]:
        try:
            os.killpg(pid, sig)
        except ProcessLookupError:
            pass
        time.sleep(delay)
    end = time.monotonic() + 2
    while os.waitpid(pid, os.WNOHANG)[0] == 0:
        if time.monotonic() >= end:
            raise RuntimeError("cleanup bound")
        time.sleep(0.05)
    pid = None
    parent.close()
    # Drop our shared description, then independently test backend release.
    os.close(lock_fd)
    fd = os.open(os.path.join(request["store"], "owner.lock"), os.O_RDWR | os.O_NOFOLLOW)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        channel.sendall(b"released\n")
    finally:
        os.close(fd)
except BaseException:
    try:
        channel.sendall(b"failed\n")
    except OSError:
        pass
finally:
    if pid is not None:
        try:
            os.killpg(pid, signal.SIGKILL)
            # No unbounded join on the failure path; surviving children retain lock.
            os.waitpid(pid, os.WNOHANG)
        except OSError:
            pass
    channel.close()
