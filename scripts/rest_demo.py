import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request


ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
HTTP_ADDR = "127.0.0.1:18081"
BASE_URL = f"http://{HTTP_ADDR}"


def request(method, path, payload=None):
    data = None
    headers = {}
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(BASE_URL + path, data=data, method=method, headers=headers)
    with urllib.request.urlopen(req, timeout=3) as resp:
        return json.loads(resp.read().decode("utf-8"))


def wait_ready():
    for _ in range(40):
        try:
            request("GET", "/health")
            return
        except Exception:
            time.sleep(0.25)
    raise RuntimeError("CoreRank REST API did not become ready")


def main():
    tmp_dir = os.path.join(ROOT, "tmp")
    os.makedirs(tmp_dir, exist_ok=True)
    exe_path = os.path.join(tmp_dir, "corerank-server.exe" if os.name == "nt" else "corerank-server")

    env = os.environ.copy()
    env["GOCACHE"] = env.get("GOCACHE", os.path.join(ROOT, ".gocache"))
    env["GRPC_ADDR"] = "127.0.0.1:18080"
    env["HTTP_ADDR"] = HTTP_ADDR
    env["METRICS_ADDR"] = "127.0.0.1:19091"

    subprocess.run(["go", "build", "-o", exe_path, "./cmd/server"], cwd=ROOT, env=env, check=True)

    proc = subprocess.Popen(
        [exe_path],
        cwd=ROOT,
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    try:
        wait_ready()
        steps = [
            ("POST", "/api/rank/score", {"player_id": "p1", "score": 1200}),
            ("POST", "/api/rank/score", {"player_id": "p2", "score": 1500}),
            ("POST", "/api/rank/score", {"player_id": "p3", "score": 1300}),
            ("GET", "/api/rank/top?n=3", None),
            ("GET", "/api/rank/player/p3", None),
            ("POST", "/api/match/pool", {"player_id": "p1", "mmr_score": 1200}),
            ("POST", "/api/match/pool", {"player_id": "p2", "mmr_score": 1210}),
        ]
        for method, path, payload in steps:
            print(f">>> {method} {path}")
            print(json.dumps(request(method, path, payload), ensure_ascii=False, indent=2))

        print(">>> POST /api/match/tickets")
        first_ticket = request("POST", "/api/match/tickets", {
            "player_id": "p4",
            "mmr_score": 1250,
            "match_mode": "duel",
            "max_wait_ms": 30000,
        })
        print(json.dumps(first_ticket, ensure_ascii=False, indent=2))

        print(">>> POST /api/match/tickets")
        second_ticket = request("POST", "/api/match/tickets", {
            "player_id": "p5",
            "mmr_score": 1260,
            "match_mode": "duel",
            "max_wait_ms": 30000,
        })
        print(json.dumps(second_ticket, ensure_ascii=False, indent=2))

        print(f">>> GET /api/match/tickets/{first_ticket['TicketID']}")
        refreshed_first = request("GET", f"/api/match/tickets/{first_ticket['TicketID']}")
        print(json.dumps(refreshed_first, ensure_ascii=False, indent=2))

        match_id = second_ticket.get("MatchID")
        if match_id:
            print(f">>> GET /api/match/results/{match_id}")
            print(json.dumps(request("GET", f"/api/match/results/{match_id}"), ensure_ascii=False, indent=2))
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()


if __name__ == "__main__":
    try:
        main()
    except urllib.error.HTTPError as exc:
        print(exc.read().decode("utf-8", errors="replace"), file=sys.stderr)
        raise
