#!/bin/bash
# deploy_web.sh - wdraża Django na VPS pathl.pl i uruchamia na porcie 80 (via iptables 80->8000)
# Uruchom lokalnie: bash www/deploy_web.sh

set -e
VPS="debian@pathl.pl"
REMOTE_DIR="/home/debian/launcher_web"

echo "==> [1/4] Pakuję i wysyłam pliki na VPS..."
tar -czf /tmp/launcher_web.tar.gz \
    --exclude='./venv' \
    --exclude='./__pycache__' \
    --exclude='./*.pyc' \
    --exclude='./db.sqlite3' \
    --exclude='./.git' \
    -C /home/neon/Laucher/www .

scp /tmp/launcher_web.tar.gz "$VPS:/tmp/launcher_web.tar.gz"
ssh "$VPS" "mkdir -p $REMOTE_DIR && tar -xzf /tmp/launcher_web.tar.gz -C $REMOTE_DIR && rm /tmp/launcher_web.tar.gz"
rm -f /tmp/launcher_web.tar.gz
echo "    Pliki wysłane."

echo "==> [2/4] Sprawdzam zależności Python na VPS..."
ssh "$VPS" "python3 -c 'import django, psycopg2, requests' 2>/dev/null && echo '    OK - pakiety zainstalowane' || (
    echo '    Instaluję pakiety...'
    python3 -m pip install --user --break-system-packages django psycopg2-binary requests 2>&1 | tail -3
)"

echo "==> [3/4] Migracje bazy danych..."
ssh "$VPS" "cd $REMOTE_DIR && python3 manage.py migrate --run-syncdb 2>&1 | tail -5"

echo "==> [4/4] Restartuję serwer Django na porcie 8000 (80 -> 8000 przez iptables)..."
ssh "$VPS" bash <<'ENDSSH'
# iptables redirect: zewnętrzne :80 -> :8000
sudo iptables -t nat -C PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8000 2>/dev/null || \
    sudo iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8000

# iptables redirect: lokalne :80 -> :8000 (dla curl localhost)
sudo iptables -t nat -C OUTPUT -p tcp --dport 80 -j REDIRECT --to-port 8000 2>/dev/null || \
    sudo iptables -t nat -A OUTPUT -p tcp --dport 80 -j REDIRECT --to-port 8000

# Zatrzymaj starą instancję
pkill -f "manage.py runserver" 2>/dev/null && sleep 1 || true

# Uruchom nową instancję w tle
cd /home/debian/launcher_web
nohup python3 manage.py runserver 0.0.0.0:8000 > /tmp/django_web.log 2>&1 &
echo "Uruchomiono Django PID=$!"
ENDSSH

sleep 3
echo ""
echo "==> Status (ostatnie logi):"
ssh "$VPS" "tail -8 /tmp/django_web.log"
echo ""
echo "✓ Gotowe! Strona dostępna pod: http://pathl.pl/"
