from django.contrib import admin
from .models import Repository, RepositoryFile, Comment, Vote, Repost, UserProfile


class RepositoryFileInline(admin.TabularInline):
    model = RepositoryFile
    extra = 1


@admin.register(Repository)
class RepositoryAdmin(admin.ModelAdmin):
    list_display  = ('name', 'owner', 'category', 'version', 'total_downloads', 'created_at')
    list_filter   = ('category', 'is_public')
    search_fields = ('name', 'description', 'tags')
    prepopulated_fields = {'slug': ('name',)}
    inlines = [RepositoryFileInline]


@admin.register(UserProfile)
class UserProfileAdmin(admin.ModelAdmin):
    list_display = ('user', 'repo_count', 'download_count')


@admin.register(Comment)
class CommentAdmin(admin.ModelAdmin):
    list_display = ('author', 'repo', 'created_at')
    list_filter  = ('repo',)


@admin.register(Vote)
class VoteAdmin(admin.ModelAdmin):
    list_display = ('user', 'repo', 'vote_type')


@admin.register(Repost)
class RepostAdmin(admin.ModelAdmin):
    list_display = ('user', 'repo', 'reposted_at')
