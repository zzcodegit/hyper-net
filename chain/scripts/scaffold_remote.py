#!/usr/bin/env python3
"""
Скрипт для создания блокчейна Hypernet (ignite scaffold) на удалённой Ubuntu-машине по SSH.
Использует тот же dostup.md, что и ssh_tests.py (формат: IP - PASSWORD).
Запуск: python chain/scripts/scaffold_remote.py [--host IP] [--download]
"""
from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

import paramiko

# Корень репозитория (где лежит dostup.md)
REPO_ROOT = Path(__file__).resolve().parent.parent.parent
DOSTUP_FILE = REPO_ROOT / "dostup.md"
REMOTE_DIR = "/opt/hypernet-node"
REMOTE_CHAIN = f"{REMOTE_DIR}/chain"
SSH_PORT = 22

# Bash-скрипт, выполняемый на удалённой машине: установка Ignite + scaffold
REMOTE_SCRIPT = r"""
set -e
export PATH="/usr/local/go/bin:$HOME/bin:/usr/local/bin:$PATH"

echo "[remote] Проверка зависимостей..."
if ! command -v go >/dev/null 2>&1; then
  echo "[remote] Go не найден. Установите Go 1.21+ на сервер."
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "[remote] Устанавливаю curl..."
  apt-get update -qq && apt-get install -y -qq curl
fi

echo "[remote] Переход в __REMOTE_CHAIN__"
mkdir -p "__REMOTE_CHAIN__"
cd "__REMOTE_CHAIN__"

if ! command -v ignite >/dev/null 2>&1; then
  echo "[remote] Ignite CLI не найден. Устанавливаю..."
  curl -sSfL https://get.ignite.com/cli | bash
  export PATH="$(pwd):$HOME/bin:/usr/local/bin:$PATH"
fi
# Установщик может положить бинарник в текущий каталог
if [ -f "./ignite" ]; then
  chmod +x ./ignite
  export PATH="$(pwd):$PATH"
fi
if ! command -v ignite >/dev/null 2>&1; then
  echo "[remote] Ignite не появился в PATH. Проверьте установку."
  exit 1
fi
echo "[remote] $(ignite version 2>&1 || true)"

if [ -d "hypernet" ]; then
  echo "[remote] Папка hypernet уже есть. Удаляю для пересоздания..."
  rm -rf hypernet
fi
echo "[remote] Запуск: ignite scaffold chain hypernet --no-module"
ignite scaffold chain hypernet --no-module
echo "[remote] Готово. Проект: __REMOTE_CHAIN__/hypernet"
"""


def parse_dostup(path: Path) -> list[tuple[str, str]]:
    servers = []
    if not path.exists():
        return servers
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "-" not in line:
            continue
        host_part, pwd_part = line.split("-", 1)
        host, password = host_part.strip(), pwd_part.strip()
        if host and password:
            servers.append((host, password))
    return servers


def run_scaffold(host: str, password: str, download: bool) -> bool:
    print(f"[local] Подключаюсь к root@{host}...")
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    try:
        client.connect(
            hostname=host,
            port=SSH_PORT,
            username="root",
            password=password,
            timeout=30,
        )
    except Exception as e:
        print(f"[local] Ошибка подключения: {e}")
        return False

    script = REMOTE_SCRIPT.replace("__REMOTE_CHAIN__", REMOTE_CHAIN)
    channel = client.get_transport().open_session()
    channel.exec_command("bash -s")
    channel.send(script)
    channel.shutdown_write()

    exit_status = None
    while not channel.exit_status_ready():
        if channel.recv_ready():
            data = channel.recv(4096).decode("utf-8", errors="replace")
            print(data, end="")
        if channel.recv_stderr_ready():
            data = channel.recv_stderr(4096).decode("utf-8", errors="replace")
            print(data, end="", file=sys.stderr)
    exit_status = channel.recv_exit_status()
    channel.close()
    client.close()

    if exit_status != 0:
        print(f"[local] Удалённый скрипт завершился с кодом {exit_status}")
        return False

    if download:
        if not _download_chain(host, password):
            return False
    return True


def _download_chain(host: str, password: str) -> bool:
    """Скачивает chain/hypernet с сервера в локальный chain/hypernet."""
    local_chain = REPO_ROOT / "chain" / "hypernet"
    remote_hypernet = f"{REMOTE_CHAIN}/hypernet"
    print(f"[local] Скачиваю {remote_hypernet} с {host} в {local_chain}...")
    client2 = paramiko.SSHClient()
    client2.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    try:
        client2.connect(hostname=host, port=SSH_PORT, username="root", password=password, timeout=30)
        sftp = client2.open_sftp()
    except Exception as e:
        print(f"[local] Не удалось подключиться для загрузки: {e}")
        return False

    try:
        if local_chain.exists():
            import shutil
            shutil.rmtree(local_chain)
        local_chain.mkdir(parents=True, exist_ok=True)

        def download_dir(remote_path: str, local_path: Path) -> None:
            for entry in sftp.listdir_attr(remote_path):
                r = f"{remote_path}/{entry.filename}"
                l = local_path / entry.filename
                if entry.st_mode is not None and (entry.st_mode & 0o170000) == 0o040000:
                    l.mkdir(parents=True, exist_ok=True)
                    download_dir(r, l)
                else:
                    l.parent.mkdir(parents=True, exist_ok=True)
                    sftp.get(r, str(l))

        download_dir(remote_hypernet, local_chain)
        print(f"[local] Готово. Локально: {local_chain}")
        return True
    except Exception as e:
        print(f"[local] Ошибка загрузки: {e}")
        return False
    finally:
        sftp.close()
        client2.close()


def main() -> int:
    parser = argparse.ArgumentParser(description="Scaffold блокчейна Hypernet на удалённой Ubuntu по SSH")
    parser.add_argument("--host", help="IP или хост (по умолчанию — первый из dostup.md)")
    parser.add_argument("--download", action="store_true", help="После scaffold скачать chain/hypernet на локальную машину")
    args = parser.parse_args()

    servers = parse_dostup(DOSTUP_FILE)
    if not servers and not args.host:
        print("[local] Файл dostup.md не найден или пуст. Укажите --host IP и пароль через переменную SSH_PASSWORD.")
        return 1

    if args.host:
        host = args.host
        password = os.environ.get("SSH_PASSWORD", "")
        if not password and servers:
            for h, p in servers:
                if h == host:
                    password = p
                    break
        if not password:
            print("[local] Задайте пароль: SSH_PASSWORD=xxx python ... или добавьте хост в dostup.md")
            return 1
        servers = [(host, password)]

    host, password = servers[0]
    ok = run_scaffold(host, password, args.download)
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
