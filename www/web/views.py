import os
import requests
from pathlib import Path
from django.shortcuts import render, redirect
from django.contrib import messages
from django.http import FileResponse, Http404

# Builds directory: ../launchers/ relative to this Django project (or set LAUNCHERS_DIR env var)
BASE_DIR = Path(__file__).resolve().parent.parent
LAUNCHERS_DIR = os.environ.get('LAUNCHERS_DIR', str(BASE_DIR / 'launchers'))

def check_api_post(endpoint, json_data):
    # Try localhost first, then fall back to production host
    try:
        r = requests.post(f"http://localhost:8080{endpoint}", json=json_data, timeout=3)
        return r
    except requests.exceptions.RequestException:
        try:
            r = requests.post(f"http://pathl.pl:8080{endpoint}", json=json_data, timeout=3)
            return r
        except requests.exceptions.RequestException:
            class FakeResponse:
                status_code = 500
                text = "Błąd połączenia z serwerem gier (API Offline)."
            return FakeResponse()

def login_view(request):
    if request.session.get('username'):
        return redirect('download')

    error = None
    if request.method == "POST":
        username = request.POST.get('username', '').strip()
        password = request.POST.get('password', '').strip()

        if not username or not password:
            error = "Wprowadź nazwę użytkownika i hasło."
        else:
            resp = check_api_post("/login", {"username": username, "password": password})
            if resp.status_code == 200:
                request.session['username'] = username
                messages.success(request, f"Witaj z powrotem, {username}!")
                return redirect('download')
            else:
                error = resp.text.strip() or "Błędne dane logowania."

    return render(request, 'web/login.html', {'error': error})

def register_view(request):
    if request.session.get('username'):
        return redirect('download')

    error = None
    if request.method == "POST":
        username = request.POST.get('username', '').strip()
        email = request.POST.get('email', '').strip()
        password = request.POST.get('password', '').strip()

        if not username or not email or not password:
            error = "Wszystkie pola są wymagane."
        else:
            resp = check_api_post("/register", {"username": username, "email": email, "password": password})
            if resp.status_code == 200:
                messages.success(request, "Rejestracja pomyślna! Możesz się teraz zalogować.")
                return redirect('login')
            else:
                error = resp.text.strip() or "Błąd podczas rejestracji."

    return render(request, 'web/register.html', {'error': error})

def download_view(request):
    if not request.session.get('username'):
        messages.warning(request, "Zaloguj się, aby uzyskać dostęp do pobierania.")
        return redirect('login')

    # Check which launcher files exist locally to display status
    linux_exists = os.path.exists(os.path.join(LAUNCHERS_DIR, 'launcher_linux'))
    windows_exists = os.path.exists(os.path.join(LAUNCHERS_DIR, 'launcher_win.exe'))

    return render(request, 'web/download.html', {
        'username': request.session.get('username'),
        'linux_exists': linux_exists,
        'windows_exists': windows_exists,
    })

def download_linux(request):
    if not request.session.get('username'):
        return redirect('login')
    path = os.path.join(LAUNCHERS_DIR, 'launcher_linux')
    if os.path.exists(path):
        return FileResponse(open(path, 'rb'), as_attachment=True, filename="launcher_linux")
    raise Http404("Launcher Linux nie został jeszcze skompilowany na serwerze.")

def download_windows(request):
    if not request.session.get('username'):
        return redirect('login')
    path = os.path.join(LAUNCHERS_DIR, 'launcher_win.exe')
    if os.path.exists(path):
        return FileResponse(open(path, 'rb'), as_attachment=True, filename="launcher_win.exe")
    raise Http404("Launcher Windows nie został jeszcze skompilowany na serwerze.")

def logout_view(request):
    request.session.flush()
    messages.info(request, "Wylogowano pomyślnie.")
    return redirect('login')
