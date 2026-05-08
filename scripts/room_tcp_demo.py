import json
import os
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request


ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))


def find_free_addr():
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        sock.bind(("127.0.0.1", 0))
        host, port = sock.getsockname()
        return f"{host}:{port}"
    finally:
        sock.close()


def request(base_url, method, path, payload=None):
    data = None
    headers = {}
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(base_url + path, data=data, method=method, headers=headers)
    with urllib.request.urlopen(req, timeout=3) as resp:
        return json.loads(resp.read().decode("utf-8"))


def wait_ready(base_url):
    for _ in range(60):
        try:
            request(base_url, "GET", "/health")
            return
        except Exception:
            time.sleep(0.25)
    raise RuntimeError("CoreRank REST API did not become ready")


def wait_roomserver_registered(base_url, match_mode, server_id):
    for _ in range(60):
        try:
            servers = request(base_url, "GET", f"/api/servers?match_mode={match_mode}")
            for server in servers:
                if server.get("server_id") == server_id:
                    return server
        except Exception:
            pass
        time.sleep(0.25)
    raise RuntimeError("roomserver did not register to CoreRank")


def build_binary(env, package, output_name):
    tmp_dir = os.path.join(ROOT, "tmp")
    os.makedirs(tmp_dir, exist_ok=True)
    exe_name = f"{output_name}.exe" if os.name == "nt" else output_name
    exe_path = os.path.join(tmp_dir, exe_name)
    subprocess.run(["go", "build", "-o", exe_path, package], cwd=ROOT, env=env, check=True)
    return exe_path


def start_process(exe_path, env):
    return subprocess.Popen(
        [exe_path],
        cwd=ROOT,
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )


class TCPClient:
    def __init__(self, addr):
        host, port = addr.rsplit(":", 1)
        self.sock = socket.create_connection((host, int(port)), timeout=3)
        self.file = self.sock.makefile("rwb")

    def send(self, payload):
        self.file.write(json.dumps(payload).encode("utf-8") + b"\n")
        self.file.flush()

    def recv(self):
        line = self.file.readline()
        if not line:
            raise RuntimeError("roomserver closed the TCP connection")
        return json.loads(line.decode("utf-8"))

    def close(self):
        self.file.close()
        self.sock.close()


def create_match(base_url, match_mode):
    suffix = f"{os.getpid()}_{int(time.time() * 1000)}"
    players = [f"p_tcp_a_{suffix}", f"p_tcp_b_{suffix}"]
    base_mmr = 30000 + (os.getpid() % 1000) * 100

    first = request(base_url, "POST", "/api/match/tickets", {
        "player_id": players[0],
        "mmr_score": base_mmr,
        "match_mode": match_mode,
        "max_wait_ms": 30000,
    })
    second = request(base_url, "POST", "/api/match/tickets", {
        "player_id": players[1],
        "mmr_score": base_mmr + 10,
        "match_mode": match_mode,
        "max_wait_ms": 30000,
    })

    match_id = second.get("MatchID")
    if not match_id:
        refreshed_first = request(base_url, "GET", f"/api/match/tickets/{first['TicketID']}")
        match_id = refreshed_first.get("MatchID")
    if not match_id:
        raise RuntimeError("match was not completed")
    result = request(base_url, "GET", f"/api/match/results/{match_id}")
    return result


def run_tcp_flow(room_addr, room_id, players):
    p1 = TCPClient(room_addr)
    p2 = TCPClient(room_addr)
    try:
        p1.send({"type": "join", "room_id": room_id, "player_id": players[0]})
        resp = p1.recv()
        assert_type(resp, "joined")
        print(f"{players[0]} joined {room_id}")

        p2.send({"type": "join", "room_id": room_id, "player_id": players[1]})
        resp = p2.recv()
        assert_type(resp, "joined")
        print(f"{players[1]} joined {room_id}")

        p1.send({"type": "ready", "room_id": room_id, "player_id": players[0]})
        resp = p1.recv()
        assert_type(resp, "ready")
        print(f"{players[0]} ready")

        p2.send({"type": "ready", "room_id": room_id, "player_id": players[1]})
        resp = p2.recv()
        assert_type(resp, "ready")
        print(f"{players[1]} ready")

        resp = p2.recv()
        assert_type(resp, "room_started")
        print(f"room_started {resp['room_id']}")

        p1.send({"type": "leave", "room_id": room_id, "player_id": players[0]})
        assert_type(p1.recv(), "left")
        p2.send({"type": "leave", "room_id": room_id, "player_id": players[1]})
        assert_type(p2.recv(), "left")
    finally:
        p1.close()
        p2.close()


def assert_type(response, expected_type):
    actual = response.get("type")
    if actual != expected_type:
        raise RuntimeError(f"expected {expected_type}, got {response}")


def terminate(proc):
    if proc is None:
        return
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()


def main():
    grpc_addr = find_free_addr()
    http_addr = find_free_addr()
    metrics_addr = find_free_addr()
    room_addr = find_free_addr()
    base_url = f"http://{http_addr}"
    match_mode = f"tcp_demo_{os.getpid()}_{int(time.time())}"
    server_id = f"tcp-room-{os.getpid()}"

    env = os.environ.copy()
    env["GOCACHE"] = env.get("GOCACHE", os.path.join(ROOT, ".gocache"))

    server_exe = build_binary(env, "./cmd/server", "corerank-server")
    room_exe = build_binary(env, "./cmd/roomserver", "corerank-roomserver")

    core_env = env.copy()
    core_env["GRPC_ADDR"] = grpc_addr
    core_env["HTTP_ADDR"] = http_addr
    core_env["METRICS_ADDR"] = metrics_addr

    room_env = env.copy()
    room_env["ROOM_SERVER_ID"] = server_id
    room_env["ROOM_SERVER_ADDR"] = room_addr
    room_env["CORE_RANK_HTTP"] = base_url
    room_env["MATCH_MODE"] = match_mode
    room_env["CAPACITY"] = "8"
    room_env["HEARTBEAT_INTERVAL_MS"] = "1000"

    core_proc = None
    room_proc = None
    try:
        core_proc = start_process(server_exe, core_env)
        wait_ready(base_url)

        room_proc = start_process(room_exe, room_env)
        wait_roomserver_registered(base_url, match_mode, server_id)

        result = create_match(base_url, match_mode)
        room_id = result["RoomID"]
        assigned_addr = result["ServerAddr"]
        players = result["PlayerIDs"]

        print(f"CoreRank matched {players[0]}/{players[1]}")
        print(f"assigned room_id={room_id} server_addr={assigned_addr}")
        if assigned_addr != room_addr:
            raise RuntimeError(f"unexpected roomserver assignment: {assigned_addr}, expected {room_addr}")

        run_tcp_flow(assigned_addr, room_id, players)
        print("TCP roomserver demo completed")
    finally:
        terminate(room_proc)
        terminate(core_proc)


if __name__ == "__main__":
    try:
        main()
    except urllib.error.HTTPError as exc:
        print(exc.read().decode("utf-8", errors="replace"), file=sys.stderr)
        raise
