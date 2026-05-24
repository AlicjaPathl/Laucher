#!/bin/bash
# start_web.sh - uruchamia Django na porcie 80
# Wymaga jednorazowego ustawienia sysctl lub uruchomienia przez sudo
cd "$(dirname "$0")"

PORT=80
HOST=0.0.0.0

echo "==> Uruchamianie serwera WWW Django na http://${HOST}:${PORT}"

# Próba uruchomienia bez roota (działa jeśli ip_unprivileged_port_start <= 80)
./venv/bin/python manage.py runserver ${HOST}:${PORT} 2>&1
