#!/usr/bin/env python3
"""Exercise the Redis command that Docker Compose passes to the container."""

import json
import os
from pathlib import Path
import subprocess
import time
import uuid


ROOT = Path(__file__).resolve().parents[2]
COMPOSE_FILES = (
    "deploy/docker-compose.yml",
    "deploy/docker-compose.local.yml",
    "deploy/docker-compose.dev.yml",
)
PASSWORDS = ("", 'quote"dollar$back`tick')
BASE_COMMAND = [
    "redis-server", "--save", "60", "1", "--appendonly", "yes",
    "--appendfsync", "everysec", "--requirepass",
]


def run(*args, env=None, check=True):
    return subprocess.run(
        args, cwd=ROOT, env=env, check=check, capture_output=True, text=True
    )


def ping_without_auth(container_id):
    # The service sets REDISCLI_AUTH even when empty; remove it to test a
    # genuinely unauthenticated client without redis-cli's empty AUTH warning.
    return run(
        "docker", "exec", container_id, "sh", "-c",
        "unset REDISCLI_AUTH; exec redis-cli ping", check=False,
    )


def assert_redis_auth(compose_file, password):
    env = os.environ.copy()
    env.update(POSTGRES_PASSWORD="ci-only", REDIS_PASSWORD=password)
    project = f"sub2api-redis-command-{uuid.uuid4().hex[:12]}"
    compose = ("docker", "compose", "-f", compose_file, "-p", project)
    try:
        run(*compose, "up", "-d", "--no-deps", "redis", env=env)
        container_id = run(*compose, "ps", "-q", "redis", env=env).stdout.strip()
        assert container_id, f"{compose_file}: Redis container did not start"
        # `docker compose config` escapes a literal `$` as `$$` for re-parsing.
        # Inspect the created container to verify the arguments Docker received.
        details = json.loads(run("docker", "inspect", container_id).stdout)[0]["Config"]
        expected = BASE_COMMAND + [password]
        assert details["Cmd"] == expected, (
            f"{compose_file}: command={details['Cmd']!r}, expected={expected!r}"
        )
        assert f"REDISCLI_AUTH={password}" in details["Env"], compose_file

        for _ in range(40):
            if password:
                response = run("docker", "exec", "-e", f"REDISCLI_AUTH={password}", container_id, "redis-cli", "ping", check=False)
            else:
                response = ping_without_auth(container_id)
            if response.stdout.strip() == "PONG":
                break
            time.sleep(0.25)
        else:
            logs = run("docker", "logs", container_id, check=False)
            raise AssertionError(f"Redis did not accept configured password: {logs.stdout}\n{logs.stderr}")

        unauthenticated = ping_without_auth(container_id)
        output = (unauthenticated.stdout + unauthenticated.stderr).strip()
        if password:
            assert "NOAUTH Authentication required." in output, output
        else:
            assert output == "PONG", output
    finally:
        run(*compose, "down", "--volumes", "--remove-orphans", env=env, check=False)


def main():
    for compose_file in COMPOSE_FILES:
        for password in PASSWORDS:
            assert_redis_auth(compose_file, password)
            print(f"{compose_file}: Redis command and authentication passed ({'empty' if not password else 'special'} password)")


if __name__ == "__main__":
    main()
