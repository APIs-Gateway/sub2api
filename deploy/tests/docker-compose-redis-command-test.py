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


def compose_redis_service(compose_file, password):
    env = os.environ.copy()
    env.update(POSTGRES_PASSWORD="ci-only", REDIS_PASSWORD=password)
    result = run("docker", "compose", "-f", compose_file, "config", "--format", "json", env=env)
    return json.loads(result.stdout)["services"]["redis"]


def assert_redis_auth(service, password):
    name = f"sub2api-redis-command-test-{uuid.uuid4().hex[:12]}"
    run("docker", "run", "--detach", "--rm", "--name", name, service["image"], *service["command"])
    try:
        for _ in range(40):
            response = run("docker", "exec", "-e", f"REDISCLI_AUTH={password}", name, "redis-cli", "ping", check=False)
            if response.stdout.strip() == "PONG":
                break
            time.sleep(0.25)
        else:
            logs = run("docker", "logs", name, check=False)
            raise AssertionError(f"Redis did not accept configured password: {logs.stdout}\n{logs.stderr}")

        unauthenticated = run("docker", "exec", name, "redis-cli", "ping", check=False)
        output = (unauthenticated.stdout + unauthenticated.stderr).strip()
        if password:
            assert "NOAUTH Authentication required." in output, output
        else:
            assert output == "PONG", output
    finally:
        run("docker", "rm", "--force", name, check=False)


def main():
    for compose_file in COMPOSE_FILES:
        for password in PASSWORDS:
            service = compose_redis_service(compose_file, password)
            assert service["command"] == BASE_COMMAND + [password], compose_file
            assert service["environment"]["REDISCLI_AUTH"] == password, compose_file
            assert_redis_auth(service, password)
            print(f"{compose_file}: Redis command and authentication passed ({'empty' if not password else 'special'} password)")


if __name__ == "__main__":
    main()
