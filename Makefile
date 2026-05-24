.PHONY: all server launcher admin client client_win admin_rel run clean stop release release_web rele deps

VPS_USER = debian
VPS_HOST = pathl.pl
VPS_DIR  = /home/debian

# =======================================================
# make all:
#   Kompiluje WSZYSTKO, wysyła na VPS, restartuje serwer
#   i stronę. Baza danych PostgreSQL zostaje nienaruszona.
# =======================================================
all:
	@python3 all.py

# --- Lokalne builde ---
server:
	mkdir -p bin
	cd server && go build -o ../bin/server main.go

launcher:
	mkdir -p bin
	cd launcher && CGO_ENABLED=1 go build -o ../bin/launcher main.go

admin:
	mkdir -p bin
	@cp -n launcher/go.sum admin/go.sum 2>/dev/null || true
	cd admin && CGO_ENABLED=1 go build -o ../bin/admin_panel main.go
	@echo "Panel admina gotowy: bin/admin_panel"
	@./bin/admin_panel &

client:
	mkdir -p bin
	@echo "==> Budowanie klienta na Linux (produkcyjny pathl.pl)..."
	cd launcher && CGO_ENABLED=1 go build \
		-ldflags "-X main.serverURL=http://pathl.pl:8080 -X main.wsURL=ws://pathl.pl:8080/ws -X main.tcpAddr=pathl.pl:8082" \
		-o ../bin/launcher_linux main.go
	@echo "Gotowe! Klient linux: bin/launcher_linux"

client_win:
	mkdir -p bin
	@echo "==> Budowanie klienta na Windows (produkcyjny pathl.pl)..."
	cd launcher && CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc go build \
		-ldflags "-X main.serverURL=http://pathl.pl:8080 -X main.wsURL=ws://pathl.pl:8080/ws -X main.tcpAddr=pathl.pl:8082" \
		-o ../bin/launcher_win.exe main.go \
		&& echo "Gotowe! Klient Windows: bin/launcher_win.exe" \
		|| echo "Kompilacja Windows nie powiodla sie."

# -----------------------------------------------
# make rele: buduje nowy launcher (Linux + Windows)
#   i wysyła go na serwer WWW. Nic innego nie rusza.
# -----------------------------------------------
rele: client client_win
	@echo "==> Wysylanie launchera Linux na $(VPS_HOST)..."
	@ssh $(VPS_USER)@$(VPS_HOST) "mkdir -p $(VPS_DIR)/launcher_web/launchers"
	@scp bin/launcher_linux $(VPS_USER)@$(VPS_HOST):$(VPS_DIR)/launcher_web/launchers/launcher_linux
	@echo "==> Wysylanie launchera Windows na $(VPS_HOST)..."
	@scp bin/launcher_win.exe $(VPS_USER)@$(VPS_HOST):$(VPS_DIR)/launcher_web/launchers/launcher_win.exe
	@echo ""
	@echo "===================================================="
	@echo "  Launcher zaktualizowany na $(VPS_HOST)!"
	@echo "  Linux:   /launchers/launcher_linux"
	@echo "  Windows: /launchers/launcher_win.exe"
	@echo "===================================================="
admin_rel:
	mkdir -p bin
	@cp -n launcher/go.sum admin/go.sum 2>/dev/null || true
	@echo "==> Budowanie panelu admina (produkcyjny pathl.pl)..."
	cd admin && CGO_ENABLED=1 go build \
		-ldflags "-X main.serverURL=http://pathl.pl:8080 -X main.tcpAddr=pathl.pl:8082 -X main.adminToken=neon-admin-secret" \
		-o ../bin/admin_rel main.go
	@echo "Panel admina produkcyjny gotowy: bin/admin_rel"

deps:
	sudo apt install -y libxxf86vm-dev gcc gcc-mingw-w64-x86-64

run: server launcher
	@echo "Uruchamianie serwera w tle..."
	@kill -9 $$(lsof -t -i:8080) 2>/dev/null || true
	@kill -9 $$(lsof -t -i:8082) 2>/dev/null || true
	@./bin/server & echo $$! > server.pid
	@sleep 2
	@echo "Uruchamianie launchera..."
	@./bin/launcher
	@echo "Zamykanie serwera..."
	@kill $$(cat server.pid 2>/dev/null) 2>/dev/null || true
	@rm -f server.pid

# -----------------------------------------------
# make release: wysyła tylko serwer Go na VPS
# -----------------------------------------------
release: server
	@echo "==> Zatrzymywanie serwera na VPS przed wysylka..."
	@ssh $(VPS_USER)@$(VPS_HOST) "pkill -f $(VPS_DIR)/server 2>/dev/null || true" || true
	@echo "==> Wysylanie serwera na $(VPS_HOST)..."
	scp bin/server $(VPS_USER)@$(VPS_HOST):$(VPS_DIR)/server
	@echo "==> Tworzenie skryptu restartu na VPS (jesli nie istnieje)..."
	@ssh $(VPS_USER)@$(VPS_HOST) 'test -f $(VPS_DIR)/restart.sh || (printf "#!/bin/bash\npkill -f $(VPS_DIR)/server 2>/dev/null || true\nsleep 1\nnohup $(VPS_DIR)/server > $(VPS_DIR)/server.log 2>&1 &\necho Started PID \$$!\n" > $(VPS_DIR)/restart.sh && chmod +x $(VPS_DIR)/restart.sh)'
	@echo "==> Restartowanie serwera na VPS..."
	@ssh $(VPS_USER)@$(VPS_HOST) 'bash $(VPS_DIR)/restart.sh' || true
	@sleep 2
	@echo ""
	@ssh $(VPS_USER)@$(VPS_HOST) 'curl -s http://localhost:8080/status'
	@echo ""
	@echo "==> Gotowe! Serwer dziala na $(VPS_HOST):8080 i :8082"
	@echo "    Logi: ssh $(VPS_USER)@$(VPS_HOST) 'tail -f $(VPS_DIR)/server.log'"

# -----------------------------------------------
# make release_web: wysyła tylko Django + launchery
# -----------------------------------------------
release_web: client client_win
	@echo "==> Wysylanie Django na VPS..."
	@tar -czf /tmp/launcher_web.tar.gz \
		--exclude='./venv' --exclude='./__pycache__' \
		--exclude='./*.pyc' --exclude='./db.sqlite3' \
		--exclude='./.git' --exclude='./launchers' \
		-C www .
	@scp /tmp/launcher_web.tar.gz $(VPS_USER)@$(VPS_HOST):/tmp/launcher_web.tar.gz
	@ssh $(VPS_USER)@$(VPS_HOST) "mkdir -p $(VPS_DIR)/launcher_web && tar -xzf /tmp/launcher_web.tar.gz -C $(VPS_DIR)/launcher_web && rm /tmp/launcher_web.tar.gz"
	@rm -f /tmp/launcher_web.tar.gz
	@echo "==> Wysylanie binarek launchera (Linux + Windows)..."
	@ssh $(VPS_USER)@$(VPS_HOST) "mkdir -p $(VPS_DIR)/launcher_web/launchers"
	@scp bin/launcher_linux $(VPS_USER)@$(VPS_HOST):$(VPS_DIR)/launcher_web/launchers/launcher_linux
	@test -f bin/launcher_win.exe && scp bin/launcher_win.exe $(VPS_USER)@$(VPS_HOST):$(VPS_DIR)/launcher_web/launchers/launcher_win.exe || true
	@echo "==> Restartowanie Django..."
	@ssh $(VPS_USER)@$(VPS_HOST) "sudo iptables -t nat -C PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8000 2>/dev/null || sudo iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8000"
	@ssh $(VPS_USER)@$(VPS_HOST) "pkill -f 'manage.py runserver' 2>/dev/null; true"
	@sleep 1
	@ssh -o ConnectTimeout=10 $(VPS_USER)@$(VPS_HOST) "cd $(VPS_DIR)/launcher_web && nohup python3 manage.py runserver 0.0.0.0:8000 >> /tmp/django_web.log 2>&1 & echo 'Django OK'"
	@echo "==> Gotowe! http://pathl.pl/"

clean:
	rm -rf bin/ server.pid
	@kill $$(cat server.pid 2>/dev/null) 2>/dev/null || true

# -----------------------------------------------
# make stop: zatrzymuje wszystko na lokalnej maszynie
#   - serwer Go (:8080, :8082)
#   - launcher
#   - PostgreSQL
# -----------------------------------------------
stop:
	@echo "==> Zatrzymywanie lokalnego serwera Go..."
	@kill $$(cat server.pid 2>/dev/null) 2>/dev/null || true
	@rm -f server.pid
	@pkill -f 'bin/server' 2>/dev/null || true
	@pkill -f 'bin/launcher' 2>/dev/null || true
	@kill -9 $$(lsof -t -i:8080) 2>/dev/null || true
	@kill -9 $$(lsof -t -i:8082) 2>/dev/null || true
	@echo "==> Zatrzymywanie PostgreSQL..."
	@sudo systemctl stop postgresql 2>/dev/null || sudo service postgresql stop 2>/dev/null || true
	@echo "==> Wszystko zatrzymane."
