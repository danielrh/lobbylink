#!/usr/bin/env python3
"""Expand the coturn relay pool and raise LobbyLink's service capacity."""

import argparse
import fcntl
import os
from pathlib import Path
import pwd
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time


MIN_PORT = 49152
MAX_PORT = 65535
PORT_COUNT = MAX_PORT - MIN_PORT + 1


def run(*command):
    subprocess.run(command, check=True)


def output(*command):
    return subprocess.check_output(command, text=True).strip()


def set_number(text, key, default, choose):
    pattern = rf"(?m)^([ \t]*{re.escape(key)}[ \t]*=[ \t]*)([^#\n]*?)(?=[ \t]*(?:#.*)?$)"
    matches = list(re.finditer(pattern, text))
    if not matches:
        return text, default
    if len(matches) > 1:
        raise ValueError(f"duplicate setting: {key}")
    value = matches[0][2]
    if not re.fullmatch(r"[+]?[0-9][0-9_]*", value):
        raise ValueError(f"expected a nonnegative integer for {key}")
    current = int(value.replace("_", ""))
    wanted = choose(current)
    if wanted != current:
        start, end = matches[0].span(2)
        text = text[:start] + str(wanted) + text[end:]
    return text, wanted


def turn_capacity(text):
    def first_port(current):
        if not MIN_PORT <= current <= MAX_PORT:
            raise ValueError("coturn's min-port is outside the default relay range")
        return MIN_PORT

    text, _ = set_number(text, "min-port", MIN_PORT, first_port)
    text, _ = set_number(text, "max-port", MAX_PORT, lambda _: MAX_PORT)
    text, quota = set_number(text, "total-quota", 0,
                             lambda n: max(n, PORT_COUNT) if n else 0)
    print(f"coturn: {PORT_COUNT:,} UDP relay ports ({MIN_PORT}–{MAX_PORT}), "
          f"total allocation quota {quota or 'unlimited'}")
    return text


def lobby_capacity(text):
    header = re.search(r"(?m)^[ \t]*\[rooms\][ \t]*(?:#.*)?$", text)
    rooms, seats = 10000, 32
    if header:
        before, body = text[:header.end()], text[header.end():]
        following = re.search(r"(?m)^[ \t]*\[", body)
        end = following.start() if following else len(body)
        section, rooms = set_number(body[:end], "max_rooms", rooms, lambda n: max(n, 2048))
        section, seats = set_number(section, "max_players_hard", seats, lambda n: max(n, 32))
        text = before + section + body[end:]
    print(f"LobbyLink: up to {rooms:,} rooms and {seats} seats per room")
    return text


def service_arguments(manager, unit):
    run(*manager, "is-active", "--quiet", unit)
    pid = int(output(*manager, "show", unit, "-p", "MainPID", "--value"))
    if pid <= 0:
        raise ValueError(f"could not find the running process for {unit}")
    return Path(f"/proc/{pid}/cmdline").read_bytes().decode().rstrip("\0").split("\0")[1:]


def check_arguments(args, unit, config, config_flags, overrides, require_config=False):
    uses_config = False
    for index, arg in enumerate(args):
        flag, equals, value = arg.partition("=")
        if flag in config_flags:
            if not equals and index + 1 < len(args):
                value = args[index + 1]
            if not value or Path(value).resolve() != config:
                raise ValueError(f"{unit} uses a different configuration file")
            uses_config = True
        if flag in overrides:
            raise ValueError(f"{unit} sets {flag} on its command line. Adjust it there first.")
    if require_config and not uses_config:
        raise ValueError(f"{unit} does not load {config}")


def file_limits(manager, unit):
    def read(prop):
        value = output(*manager, "show", unit, "-p", prop, "--value")
        return float("inf") if value == "infinity" else int(value)

    return read("LimitNOFILESoft"), read("LimitNOFILE")


def service_edits(manager, unit, config, transform, dropin, fd_floor):
    original = config.read_bytes()
    updated = transform(original.decode()).encode()
    edits = {config: updated} if updated != original else {}
    soft, hard = file_limits(manager, unit)
    if soft < fd_floor:
        hard_value = "infinity" if hard == float("inf") else str(max(hard, fd_floor))
        edits[dropin] = f"[Service]\nLimitNOFILE={fd_floor}:{hard_value}\n".encode()
    print(f"{unit}: soft open-file limit {soft} → {max(soft, fd_floor)}")
    for path in edits:
        print(f"  update {path}")
    return edits


def write_file(path, data, attrs, owner):
    fd, name = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.")
    try:
        with os.fdopen(fd, "wb") as temporary:
            temporary.write(data)
            if attrs:
                os.fchown(temporary.fileno(), attrs.st_uid, attrs.st_gid)
                os.fchmod(temporary.fileno(), stat.S_IMODE(attrs.st_mode))
            else:
                os.fchown(temporary.fileno(), *owner)
                os.fchmod(temporary.fileno(), 0o644)
        os.replace(name, path)
    finally:
        Path(name).unlink(missing_ok=True)


def apply(plans, user, user_dropin, ufw, rule):
    edits = {path: data for _, _, changes, _ in plans for path, data in changes.items()}
    if ufw:
        run(ufw, "allow", rule)
    if not edits:
        print("Capacity settings already meet the targets.")
        return

    backup_dir = Path(tempfile.mkdtemp(prefix="lobbylink-capacity-", dir="/var/backups"))
    snapshots = {}
    for path in edits:
        if path.is_symlink():
            raise ValueError(f"refusing to replace a symlink: {path}")
        if path.exists():
            copy = backup_dir / f"{len(snapshots)}-{path.name}"
            shutil.copyfile(path, copy)
            snapshots[path] = (copy, path.stat())
        else:
            snapshots[path] = (None, None)
    print(f"Config backups: {backup_dir}", flush=True)

    changed = []
    restarted = []
    try:
        for path, data in edits.items():
            owner = (0, 0)
            if path == user_dropin:
                run("runuser", "-u", user.pw_name, "--", "mkdir", "-p", str(path.parent))
                owner = (user.pw_uid, user.pw_gid)
            else:
                path.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
            write_file(path, data, snapshots[path][1], owner)
            changed.append(path)
        for manager, unit, changes, fd_floor in plans:
            if changes:
                restarted.append((manager, unit))
                run(*manager, "daemon-reload")
                run(*manager, "restart", unit)
        time.sleep(1)
        for manager, unit, changes, fd_floor in plans:
            if changes:
                run(*manager, "is-active", "--quiet", unit)
                if file_limits(manager, unit)[0] < fd_floor:
                    raise ValueError(f"{unit} did not pick up the open-file limit")
    except (Exception, KeyboardInterrupt):
        print("Apply failed. Restoring service configuration.", file=sys.stderr)
        for path in reversed(changed):
            copy, attrs = snapshots[path]
            try:
                if copy:
                    write_file(path, copy.read_bytes(), attrs, (0, 0))
                else:
                    path.unlink(missing_ok=True)
            except OSError as error:
                print(f"Could not restore {path}: {error}", file=sys.stderr)
        for manager, unit in restarted:
            subprocess.run((*manager, "daemon-reload"))
            subprocess.run((*manager, "restart", unit))
        raise


def main(args):
    user = pwd.getpwnam(args.user)
    if user.pw_uid == 0:
        raise ValueError("specify the LobbyLink service account with --user")
    home = Path(user.pw_dir)
    runtime = f"/run/user/{user.pw_uid}"
    systemctl = ("systemctl",)
    userctl = ("runuser", "-u", user.pw_name, "--", "env", f"XDG_RUNTIME_DIR={runtime}",
               f"DBUS_SESSION_BUS_ADDRESS=unix:path={runtime}/bus", "systemctl", "--user")
    turn_unit, lobby_unit = "coturn.service", "lobbylink.service"
    turn_config = Path("/etc/turnserver.conf").resolve(strict=True)
    lobby_config = (home / "lobbylink/config.toml").resolve(strict=True)
    check_arguments(service_arguments(systemctl, turn_unit), turn_unit, turn_config,
                    {"-c", "--config"}, {"-n", "--min-port", "--max-port", "-Q", "--total-quota"})
    check_arguments(service_arguments(userctl, lobby_unit), lobby_unit, lobby_config,
                    {"-config", "--config"}, {"-max-rooms", "--max-rooms"}, require_config=True)

    name = "90-lobbylink-capacity.conf"
    turn_dropin = Path(f"/etc/systemd/system/{turn_unit}.d/{name}")
    user_dropin = home / f".config/systemd/user/{lobby_unit}.d/{name}"
    turn_edits = service_edits(systemctl, turn_unit, turn_config, turn_capacity, turn_dropin, 65536)
    lobby_edits = service_edits(userctl, lobby_unit, lobby_config, lobby_capacity, user_dropin, 8192)

    rule = f"{MIN_PORT}:{MAX_PORT}/udp"
    ufw = shutil.which("ufw")
    if ufw and "Status: active" not in output("env", "LC_ALL=C", ufw, "status"):
        ufw = None
    if ufw:
        print(f"UFW: allow {rule}")
    print(f"Allow inbound {rule} at the provider firewall if one is configured.")
    if args.apply:
        plans = [(systemctl, turn_unit, turn_edits, 65536), (userctl, lobby_unit, lobby_edits, 8192)]
        apply(plans, user, user_dropin, ufw, rule)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--user", default=os.environ.get("SUDO_USER"), help="LobbyLink systemd user")
    parser.add_argument("--apply", action="store_true", help="write changes and restart affected services")
    args = parser.parse_args()
    if os.geteuid() != 0 or not args.user:
        parser.error("run with sudo or specify the service account with --user when running as root")
    os.umask(0o077)
    try:
        if args.apply:
            with open("/run/lock/lobbylink-capacity.lock", "a") as lock:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                main(args)
        else:
            main(args)
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        sys.exit(f"increase-capacity: {error}")
