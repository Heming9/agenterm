#!/usr/bin/env python3
"""Exercise the public CLI over a persistent real DataChannel (no internet)."""
import argparse
from contextlib import contextmanager
import hashlib
import json
import math
import os
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import tempfile
import threading
import time
import signaling_server


def main(binary):
    root = Path(tempfile.mkdtemp(prefix="p2p-cli-e2e-"))
    config = dict(signal_url="http://127.0.0.1", session=secrets.token_hex(16), token=secrets.token_hex(32),
                  stun="none", bind_ip="127.0.0.1", timeout_seconds=60, register=False)
    server = signaling_server.make_server(config)
    config["signal_url"] += f":{server.server_port}"
    threading.Thread(target=server.serve_forever, daemon=True).start()
    states = {name: root / name for name in ("A", "B")}
    for name in states:
        (root / f"{name}.json").write_text(json.dumps(dict(config, client_id=name, offerer=name == "A")))

    def cli(name, *args, ok=True, timeout=70):
        options = {}
        capture = None
        if os.name == 'nt' and args[0] == 'start' and '--foreground' not in args:
            import msvcrt
            reader, writer = os.pipe()
            os.set_inheritable(writer, True)
            startup = subprocess.STARTUPINFO()
            startup.lpAttributeList = {'handle_list': [msvcrt.get_osfhandle(writer)]}
            options = dict(startupinfo=startup, close_fds=True)
            capture = (reader, writer)
        try:
            result = subprocess.run([binary, *args, "--state-dir", str(states[name])], capture_output=True, text=True, encoding="utf-8", timeout=timeout, **options)
        finally:
            if capture is not None:
                os.close(capture[1])
                drained = threading.Event()
                receipt = []
                def drain():
                    try:
                        receipt.append(os.read(capture[0], 1))
                    finally:
                        os.close(capture[0])
                        drained.set()
                threading.Thread(target=drain, daemon=True).start()
                assert drained.wait(2) and receipt == [b''], 'background worker inherited an unrelated parent pipe'
        assert (result.returncode == 0) == ok, (args, result.returncode, result.stdout, result.stderr)
        return json.loads(result.stdout) if ok else result

    def connected():
        deadline = time.monotonic() + 12
        while time.monotonic() < deadline:
            a, b = cli("A", "status"), cli("B", "status")
            if a["state"] == b["state"] == "connected":
                assert a["peer"]["id"] == "B" and b["peer"]["id"] == "A"
                return
            time.sleep(.05)
        raise AssertionError((a, b))

    try:
        assert cli("A", "status")["state"] == "stopped"
        cli("A", "start", "--config", str(root / "A.json"))
        cli("A", "start", "--config", str(root / "A.json"))  # Same session is idempotent.
        cli("B", "start", "--config", str(root / "B.json"))
        connected()
        basic = cli("A", "status")
        assert set(basic) == {"state", "client", "peer", "copy"}, basic
        assert set(basic["client"]) == {"id", "hostname"}, basic
        assert basic["peer"]["state"] == "connected", basic
        for option in ("--verbose", "-v"):
            detailed = cli("A", "status", option)
            assert detailed["session"] == config["session"] and detailed["executable"], detailed
            assert detailed["client"]["cwd"] and detailed["peer"]["platform"], detailed
            assert detailed["connection"]["peer"] == "connected", detailed
            assert "peer_observed_ms_ago" in detailed["connection"] and "local" not in detailed["connection"], detailed
        cli("A", "status", "--unknown", ok=False)
        cli("A","stop","--session",secrets.token_hex(16))
        connected()
        original_pid = json.loads((states["A"] / "daemon.json").read_text())["pid"]
        wrong = root / "wrong.json"
        wrong.write_text(json.dumps(dict(config, session=secrets.token_hex(16))))
        cli("A", "start", "--config", str(wrong), ok=False)

        source = root / "源文件 with spaces.bin"
        source.write_bytes(os.urandom(2 * 1024 * 1024 + 3))
        destination, returned = root / "received.bin", root / "returned.bin"
        expected = hashlib.sha256(source.read_bytes()).hexdigest()
        result = cli("A", "cp", f"A:{source}", f"B:{destination}")
        assert result["sha256"] == expected and result["receiver_committed"]
        result = cli("A", "cp", f"B:{destination}", f"A:{returned}")
        assert result["sha256"] == expected and returned.read_bytes() == source.read_bytes()
        # A completed receive can immediately be followed by a send, without
        # waiting for the peer's event loop or creating another connection.
        for i in range(4):
            source_id,target_id=("A","B") if i%2==0 else ("B","A")
            result=cli("A","cp",f"{source_id}:{source}",f"{target_id}:{root / ('repeat-'+str(i))}")
            assert result["sha256"]==expected
        cli("B", "cp", f"A:{source}", f"B:{destination}", ok=False)
        assert destination.read_bytes() == source.read_bytes()
        destination.write_bytes(b"old")
        cli("B", "cp", f"A:{source}", f"B:{destination}", "--overwrite")
        assert hashlib.sha256(destination.read_bytes()).hexdigest() == expected
        empty = root / "empty"
        empty.write_bytes(b"")
        cli("B", "cp", f"A:{empty}", f"B:{root / 'empty-copy'}")
        cli("A", "cp", f"A:{root}", f"B:{root / 'directory-copy'}", ok=False)
        cli("A", "cp", f"C:{source}", f"B:{root / 'wrong-peer'}", ok=False)
        assert json.loads((states["A"] / "daemon.json").read_text())["pid"] == original_pid

        endpoint = json.loads((states["A"] / "daemon.json").read_text())
        with socket.create_connection(("127.0.0.1", endpoint["port"]), timeout=3) as sock:
            sock.sendall(b'{"op":"stop","token":"wrong"}\n')
            assert json.loads(sock.makefile("rb").readline())["error"] == "unauthorized"
        connected()
        print("persistent connection, both copy directions, overwrite, identity and IPC authorization passed", flush=True)

        large = root / "large"
        with large.open("wb") as file:
            file.truncate(512 * 1024 * 1024)
        copying = subprocess.Popen([binary, "cp", f"A:{large}", f"B:{root / 'unfinished'}", "--state-dir", str(states["A"])], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 8
            while not any(root.glob(".agenterm-*.part")):
                assert copying.poll() is None, copying.communicate()
                assert time.monotonic() < deadline
                time.sleep(.01)
            deadline = time.monotonic() + 5
            while True:
                progress = cli("B", "status")["copy"]
                detailed = cli("A", "status", "--verbose")["copy"]
                if progress and detailed and progress["bytes"] > 0 and detailed["bytes"] > 0:
                    break
                assert copying.poll() is None and time.monotonic() < deadline
                time.sleep(.01)
            assert set(progress) == {"bytes", "total", "bytes_per_second"}, progress
            assert math.isfinite(progress["bytes_per_second"]) and progress["bytes_per_second"] > 0, progress
            for field in ("bytes_per_second", "average_bytes_per_second"):
                assert math.isfinite(detailed[field]) and detailed[field] > 0, detailed
            result = cli("B", "stop")
            assert result["peer_confirmed"], result
            assert not (states["B"] / "daemon.json").exists(), "stop returned before local cleanup"
            assert copying.wait(timeout=8) != 0
        finally:
            if copying.poll() is None:
                copying.kill()
                copying.wait()
        deadline = time.monotonic() + 5
        while any((state / "daemon.json").exists() for state in states.values()):
            assert time.monotonic() < deadline
            time.sleep(.025)
        assert not (root / "unfinished").exists()
        assert not list(root.glob(".agenterm-*.part"))
        assert cli("A", "status")["state"] == "stopped"
        cli("A", "stop")
        print("stop canceled the copy, confirmed both peers and cleaned temporary files", flush=True)
        large.unlink()
        # The SDP-only fixture rejects registration. Omission of `register`
        # must default to the real protocol and preserve the failure on disk.
        failed_config = dict(config, timeout_seconds=1)
        failed_config.pop("register")
        (root / "failed.json").write_text(json.dumps(failed_config))
        cli("A", "start", "--config", str(root / "failed.json"), "--foreground", ok=False)
        failed = cli("A", "status")
        assert failed["state"] == "failed" and failed["client"] is None, failed
        assert "registration" in failed["error"], failed
        detailed = cli("A", "status", "--verbose")
        assert detailed["error"] == failed["error"] and "connection" in detailed, detailed
        print(json.dumps(dict(artifacts=str(root), passed=True)), flush=True)
    finally:
        for name in states:
            subprocess.run([binary, "stop", "--state-dir", str(states[name])], capture_output=True, timeout=12)
        server.shutdown()
        server.server_close()


@contextmanager
def regression_connection(binary, *, delay_seconds=0, receive_limit=None, cancel_file=False):
    root = Path(tempfile.mkdtemp(prefix="p2p-cli-regression-"))
    config = dict(signal_url="http://127.0.0.1", session=secrets.token_hex(16), token=secrets.token_hex(32),
                  stun="none", bind_ip="127.0.0.1", timeout_seconds=120, register=False)
    server = signaling_server.make_server(config)
    config["signal_url"] += f":{server.server_port}"
    threading.Thread(target=server.serve_forever, daemon=True).start()
    states = {name: root / name for name in ("A", "B")}
    for name in states:
        endpoint = dict(config, client_id=name, offerer=name == "A")
        if name == "B" and receive_limit is not None:
            endpoint["max_bytes"] = receive_limit
        if name == "B" and cancel_file:
            endpoint["cancel_file"] = str(root / "cancel")
        (root / f"{name}.json").write_text(json.dumps(endpoint))

    def cli(name, *args, ok=True, timeout=130):
        result = subprocess.run([binary, *args, "--state-dir", str(states[name])],
                                capture_output=True, text=True, encoding="utf-8", timeout=timeout)
        assert (result.returncode == 0) == ok, (args, result.returncode, result.stdout, result.stderr)
        return json.loads(result.stdout) if ok else result

    try:
        cli("A", "start", "--config", str(root / "A.json"))
        if delay_seconds:
            time.sleep(delay_seconds)
        cli("B", "start", "--config", str(root / "B.json"))
        deadline = time.monotonic() + 12
        while True:
            a, b = cli("A", "status"), cli("B", "status")
            if a["state"] == b["state"] == "connected":
                break
            assert time.monotonic() < deadline, (a, b)
            time.sleep(.05)
        yield root, states, cli
    finally:
        for name in states:
            subprocess.run([binary, "stop", "--state-dir", str(states[name])],
                           capture_output=True, timeout=12)
        server.shutdown()
        server.server_close()


def checksum(path):
    with path.open("rb") as file:
        return hashlib.file_digest(file, "sha256").hexdigest()


def assert_copy_receipt(result, size):
    assert result["receiver_committed"], result
    assert result["bytes"] == result["total"] == size, result
    assert result["seconds"] > 0, result
    for field in ("bytes_per_second", "average_bytes_per_second"):
        assert math.isfinite(result[field]) and result[field] >= 0, result
    if size:
        assert result["average_bytes_per_second"] > 0, result


def copy_file(binary, root, states, source, destination):
    # Spool progress to disk so sampling cannot deadlock a full output pipe.
    with (root / "copy.stdout").open("wb") as stdout, (root / "copy.stderr").open("wb") as stderr:
        copy = subprocess.Popen([binary, "cp", f"A:{source}", f"B:{destination}",
                                 "--state-dir", str(states["A"])], stdout=stdout, stderr=stderr)
        peak_kib = {}
        deadline = time.monotonic() + 130
        try:
            while copy.poll() is None:
                assert time.monotonic() < deadline, "large file copy timed out"
                if os.name != "nt":
                    for name, state in states.items():
                        pid = json.loads((state / "daemon.json").read_text())["pid"]
                        rss = subprocess.run(["ps", "-o", "rss=", "-p", str(pid)],
                                             capture_output=True, text=True).stdout.strip()
                        if rss:
                            peak_kib[name] = max(peak_kib.get(name, 0), int(rss))
                time.sleep(.1)
            assert copy.returncode == 0, (root / "copy.stderr").read_text()
        finally:
            if copy.poll() is None:
                copy.kill()
            copy.wait()
    result = json.loads((root / "copy.stdout").read_text())
    # Commit confirmation acknowledges the tail even below the progress window.
    assert_copy_receipt(result, source.stat().st_size)
    assert destination.stat().st_size == source.stat().st_size
    assert result["sha256"] == checksum(source) == checksum(destination)
    assert not list(root.glob(".agenterm-*.part"))
    return {name: round(kib / 1024, 2) for name, kib in peak_kib.items()}


def fifo_source_regressions(binary, root, states, cli):
    if os.name == "nt":
        return
    source = root / "source.fifo"
    os.mkfifo(source)
    try:
        # Exercise source opening in both the RPC caller and the remote peer.
        for source_id in ("A", "B"):
            target_id = "B" if source_id == "A" else "A"
            destination = root / f"fifo-copy-{source_id}"
            copy = subprocess.Popen([binary, "cp", f"{source_id}:{source}",
                                     f"{target_id}:{destination}", "--state-dir", str(states["A"])],
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                stdout, stderr = copy.communicate(timeout=3)
                assert copy.returncode != 0 and "regular file" in stderr, (stdout, stderr)
                assert not destination.exists() and not list(root.glob(".agenterm-*.part"))
                assert all(cli(name, "status", timeout=3)["state"] == "connected" for name in states)
            finally:
                # A regressed blocking open must be released before fixture stop.
                # Keep both FIFO ends open until the copy RPC has actually exited.
                release = os.open(source, os.O_RDWR | os.O_NONBLOCK)
                try:
                    try:
                        copy.communicate(timeout=5)
                    except subprocess.TimeoutExpired:
                        copy.kill()
                        copy.communicate()
                finally:
                    os.close(release)
        print("local and remote FIFO sources rejected without blocking the connection", flush=True)
    finally:
        source.unlink()


def delayed_start(binary):
    with regression_connection(binary, delay_seconds=45) as (root, states, cli):
        source, destination = root / "source", root / "received"
        source.write_bytes(os.urandom(1024 * 1024))
        # The receiver is also the connection offerer; file direction is independent.
        result = cli("A", "cp", f"B:{source}", f"A:{destination}")
        assert result["sha256"] == checksum(source) == checksum(destination)
        assert_copy_receipt(result, source.stat().st_size)
        print("45-second delayed peer startup and receiver-created connection passed", flush=True)


def transfer_regressions(binary):
    with regression_connection(binary) as (root, states, cli):
        source, destination = root / "source", root / "received"
        peak = {}
        for size in (1, 16 * 1024 + 1, 128 * 1024 + 3, 32 * 1024 * 1024, 256 * 1024 * 1024):
            with source.open("wb") as file:
                remaining = size
                while remaining:
                    chunk = os.urandom(min(remaining, 65536))
                    file.write(chunk)
                    remaining -= len(chunk)
            sampled = copy_file(binary, root, states, source, destination)
            peak.update({name: max(peak.get(name, 0), value) for name, value in sampled.items()})
            destination.unlink()
            assert cli("A", "status")["state"] == "connected"
            print(json.dumps(dict(case="file_copy", bytes=size, peak_rss_mib=sampled, passed=True)), flush=True)
        source.unlink()
        fifo_source_regressions(binary, root, states, cli)
        print(json.dumps(dict(case="large_file_memory", peak_rss_mib=peak, passed=True)), flush=True)

    with regression_connection(binary, receive_limit=1) as (root, states, cli):
        source, destination = root / "source", root / "received"
        source.write_bytes(b"too large")
        result = cli("A", "cp", f"A:{source}", f"B:{destination}", ok=False)
        assert "limit" in result.stderr or "limit" in result.stdout
        assert not destination.exists() and not list(root.glob(".agenterm-*.part"))
        source.write_bytes(b"")
        cli("A", "cp", f"A:{source}", f"B:{destination}")
        assert destination.read_bytes() == b""
        print("receive limit failure cleaned up and preserved the connection", flush=True)

    for cancel_by_file in (True, False):
        with regression_connection(binary, cancel_file=cancel_by_file) as (root, states, cli):
            source, destination = root / "source", root / "unfinished"
            with source.open("wb") as file:
                file.truncate(512 * 1024 * 1024)
            copy = subprocess.Popen([binary, "cp", f"A:{source}", f"B:{destination}",
                                     "--state-dir", str(states["A"])],
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                deadline = time.monotonic() + 8
                while not any(root.glob(".agenterm-*.part")):
                    assert copy.poll() is None, copy.communicate()
                    assert time.monotonic() < deadline
                    time.sleep(.01)
                if cancel_by_file:
                    (root / "cancel").touch()
                elif os.name == "nt":
                    copy.terminate()
                else:
                    copy.send_signal(signal.SIGINT)
                assert copy.wait(timeout=8) != 0
                deadline = time.monotonic() + 8
                while list(root.glob(".agenterm-*.part")) or any(cli(name, "status").get("copy") for name in states):
                    assert time.monotonic() < deadline, "copy cancellation did not finish"
                    time.sleep(.05)
                assert not destination.exists()
                if not cancel_by_file:
                    assert all(cli(name, "status")["state"] == "connected" for name in states)
                source.unlink()
                print("cancel file cleaned up the copy" if cancel_by_file else "interrupted copy caller cleaned up and preserved the connection", flush=True)
            finally:
                if copy.poll() is None:
                    copy.kill()
                copy.communicate()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("--delayed-only", action="store_true", help="Test a peer starting 45 seconds late")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    if not args.delayed_only:
        main(binary)
        transfer_regressions(binary)
    delayed_start(binary)
