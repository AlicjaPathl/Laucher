from django.urls import path
from django.shortcuts import redirect
from . import views

urlpatterns = [
    path('', lambda r: redirect('login'), name='root'),
    path('login/', views.login_view, name='login'),
    path('register/', views.register_view, name='register'),
    path('download/', views.download_view, name='download'),
    path('download/linux/', views.download_linux, name='download_linux'),
    path('download/windows/', views.download_windows, name='download_windows'),
    path('logout/', views.logout_view, name='logout'),
]
