#!/usr/bin/env python3
import subprocess
import sys
import os
import time
import argparse

VPS_USER = "debian"
VPS_HOST = "pathl.pl"
VPS_DIR = "/home/debian"

def run_cmd(cmd, shell=True, check=False, capture_output=False, timeout=None):
    """Uruchamia polecenie i zwraca wynik"""
    print(f"\n🔧 Wykonuję: {cmd}")
    try:
        if capture_output:
            result = subprocess.run(cmd, shell=shell, capture_output=True, text=True, 
                                  check=check, timeout=timeout)
            if result.stdout:
                print(result.stdout.strip())
            return result
        else:
            subprocess.run(cmd, shell=shell, check=check, timeout=timeout)
            return None
    except subprocess.TimeoutExpired:
        print("⏰ Timeout - kontynuuję...")
        return None
    except subprocess.CalledProcessError as e:
        print(f"❌ Błąd: {e}")
        if not check:
            return None
        raise

def build_server():
    print("\n📦 Budowanie serwera Go...")
    os.makedirs("bin", exist_ok=True)
    run_cmd("cd server && go build -o ../bin/server main.go", check=True)
    print("✅ Serwer Go zbudowany!")

def build_client_linux():
    print("\n🐧 Budowanie klienta Linux...")
    os.makedirs("bin", exist_ok=True)
    run_cmd('''cd launcher && CGO_ENABLED=1 go build \
        -ldflags "-X main.serverURL=http://pathl.pl:8080 -X main.wsURL=ws://pathl.pl:8080/ws -X main.tcpAddr=pathl.pl:8082" \
        -o ../bin/launcher_linux main.go''', check=True)
    print("✅ Klient Linux gotowy!")

def build_client_windows():
    print("\n🪟 Budowanie klienta Windows...")
    os.makedirs("bin", exist_ok=True)
    result = run_cmd('''cd launcher && CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc go build \
        -ldflags "-X main.serverURL=http://pathl.pl:8080 -X main.wsURL=ws://pathl.pl:8080/ws -X main.tcpAddr=pathl.pl:8082" \
        -o ../bin/launcher_win.exe main.go''', capture_output=True)
    if result and result.returncode == 0:
        print("✅ Klient Windows gotowy!")
    else:
        print("⚠️  Kompilacja Windows nie powiodła się")

def deploy_server():
    print("\n" + "="*60)
    print("🚀 Wysyłanie serwera Go na VPS...")
    
    run_cmd(f'ssh {VPS_USER}@{VPS_HOST} "pkill -f {VPS_DIR}/server 2>/dev/null; exit 0"')
    run_cmd(f"scp bin/server {VPS_USER}@{VPS_HOST}:{VPS_DIR}/server", check=True)
    run_cmd(f"ssh {VPS_USER}@{VPS_HOST} 'bash {VPS_DIR}/restart.sh'")
    
    time.sleep(2)
    
    result = run_cmd(f"ssh {VPS_USER}@{VPS_HOST} 'curl -s http://localhost:8080/status'", 
                     capture_output=True)
    if result and "OK" in result.stdout:
        print("✅ Serwer Go działa!")
    else:
        print("❌ Problem z serwerem Go!")

def deploy_django():
    print("\n📦 Wysyłanie Django...")
    
    run_cmd("tar -czf /tmp/launcher_web.tar.gz "
            "--exclude='./venv' --exclude='./__pycache__' "
            "--exclude='./*.pyc' --exclude='./db.sqlite3' "
            "--exclude='./.git' --exclude='./launchers' "
            "-C www .", check=True)
    
    run_cmd(f"scp /tmp/launcher_web.tar.gz {VPS_USER}@{VPS_HOST}:/tmp/launcher_web.tar.gz", check=True)
    run_cmd(f'ssh {VPS_USER}@{VPS_HOST} "mkdir -p {VPS_DIR}/launcher_web && '
            f'tar -xzf /tmp/launcher_web.tar.gz -C {VPS_DIR}/launcher_web && '
            f'rm /tmp/launcher_web.tar.gz"', check=True)
    
    os.remove("/tmp/launcher_web.tar.gz")
    print("✅ Django wysłane!")

def deploy_launchers():
    print("\n🎮 Wysyłanie binarek launchera...")
    
    run_cmd(f'ssh {VPS_USER}@{VPS_HOST} "mkdir -p {VPS_DIR}/launcher_web/launchers"')
    
    if os.path.exists("bin/launcher_linux"):
        run_cmd(f"scp bin/launcher_linux {VPS_USER}@{VPS_HOST}:{VPS_DIR}/launcher_web/launchers/launcher_linux", 
                check=True)
        print("✅ Launcher Linux wysłany!")
    else:
        print("❌ Brak bin/launcher_linux!")
    
    if os.path.exists("bin/launcher_win.exe"):
        run_cmd(f"scp bin/launcher_win.exe {VPS_USER}@{VPS_HOST}:{VPS_DIR}/launcher_web/launchers/launcher_win.exe", 
                check=True)
        print("✅ Launcher Windows wysłany!")
    else:
        print("⚠️  Brak bin/launcher_win.exe - pomijam")
def restart_django():
    print("\n🔄 Restartowanie Django...")
    
    # Uruchom skrypt na VPS
    result = run_cmd(f'ssh -o ConnectTimeout=5 {VPS_USER}@{VPS_HOST} '
                    f'"bash {VPS_DIR}/restart_django.sh"', 
                    capture_output=True, timeout=15)
    
    if result:
        print(result.stdout)
        if "200" in result.stdout or "301" in result.stdout:
            print("✅ Django działa!")
        else:
            print("⚠️  Django uruchomione, kod:", result.stdout.strip())
    else:
        print("✅ Django uruchomione")
def deploy_all():
    """Pełny deployment wszystkiego"""
    print("=" * 60)
    print("🚀 DEPLOYMENT PEŁNY - START")
    print("=" * 60)
    
    # Budowanie
    build_server()
    build_client_linux()
    build_client_windows()
    
    # Deployment
    deploy_server()
    deploy_django()
    deploy_launchers()
    restart_django()
    
    show_summary()

def deploy_django_only():
    """Deployment tylko Django + launchery"""
    print("=" * 60)
    print("🌐 DEPLOYMENT DJANGO + LAUNCHERY")
    print("=" * 60)
    
    build_client_linux()
    build_client_windows()
    deploy_django()
    deploy_launchers()
    restart_django()
    
    print("\n✅ Django i launchery zaktualizowane!")
    print(f"🌍 Strona: http://{VPS_HOST}/")

def deploy_windows_only():
    """Deployment tylko build Windows"""
    print("=" * 60)
    print("🪟 DEPLOYMENT WINDOWS")
    print("=" * 60)
    
    build_client_windows()
    deploy_launchers()
    
    print("\n✅ Windows zaktualizowany!")

def deploy_linux_only():
    """Deployment tylko build Linux"""
    print("=" * 60)
    print("🐧 DEPLOYMENT LINUX")
    print("=" * 60)
    
    build_client_linux()
    deploy_launchers()
    
    print("\n✅ Linux zaktualizowany!")

def deploy_server_only():
    """Deployment tylko serwer Go"""
    print("=" * 60)
    print("🔧 DEPLOYMENT SERWER GO")
    print("=" * 60)
    
    build_server()
    deploy_server()
    
    print("\n✅ Serwer Go zaktualizowany!")
    print(f"🔧 Status: http://{VPS_HOST}:8080/status")

def show_summary():
    """Wyświetla podsumowanie deploymentu"""
    print("\n" + "=" * 60)
    print("✅ DEPLOYMENT ZAKOŃCZONY!")
    print("=" * 60)
    print(f"🌐 Serwer Go:  http://{VPS_HOST}:8080")
    print(f"🌍 Strona WWW: http://{VPS_HOST}/")
    print(f"📋 Logi Django: ssh {VPS_USER}@{VPS_HOST} 'tail -f /tmp/django_web.log'")
    print("=" * 60)

def main():
    parser = argparse.ArgumentParser(description='Deployment tool dla Laucher')
    parser.add_argument('target', 
                       nargs='?', 
                       default='all',
                       choices=['all', 'django', 'windows', 'linux', 'server'],
                       help='Co deployować (domyślnie: all)')
    
    args = parser.parse_args()
    
    try:
        if args.target == 'all':
            deploy_all()
        elif args.target == 'django':
            deploy_django_only()
        elif args.target == 'windows':
            deploy_windows_only()
        elif args.target == 'linux':
            deploy_linux_only()
        elif args.target == 'server':
            deploy_server_only()
    except KeyboardInterrupt:
        print("\n\n⚠️  Deployment przerwany przez użytkownika")
        sys.exit(1)
    except Exception as e:
        print(f"\n❌ Deployment PRZERWANY: {e}")
        sys.exit(1)

if __name__ == "__main__":
    main()