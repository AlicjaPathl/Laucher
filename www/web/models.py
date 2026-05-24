from django.db import models
from django.contrib.auth.models import User
from django.utils import timezone


CATEGORY_CHOICES = [
    ('game',     'Gra'),
    ('program',  'Program'),
    ('tool',     'Narzędzie'),
    ('library',  'Biblioteka'),
    ('include',  'Include / Header'),
    ('cli',      'Narzędzie CLI'),
    ('other',    'Inne'),
]


class UserProfile(models.Model):
    user       = models.OneToOneField(User, on_delete=models.CASCADE, related_name='profile')
    bio        = models.TextField(blank=True, default='')
    avatar_url = models.URLField(blank=True, default='')
    website    = models.URLField(blank=True, default='')
    created_at = models.DateTimeField(default=timezone.now)

    def __str__(self):
        return f'Profil: {self.user.username}'

    def download_count(self):
        return sum(f.download_count for r in self.user.repos.all() for f in r.files.all())

    def repo_count(self):
        return self.user.repos.count()


class Repository(models.Model):
    owner        = models.ForeignKey(User, on_delete=models.CASCADE, related_name='repos')
    name         = models.CharField(max_length=120)
    slug         = models.SlugField(max_length=140, unique=True)
    description  = models.TextField(blank=True, default='')
    category     = models.CharField(max_length=20, choices=CATEGORY_CHOICES, default='other')
    version      = models.CharField(max_length=50, default='1.0.0')
    is_public    = models.BooleanField(default=True)
    created_at   = models.DateTimeField(default=timezone.now)
    updated_at   = models.DateTimeField(auto_now=True)
    tags         = models.CharField(max_length=300, blank=True, default='',
                                    help_text='Tagi oddzielone przecinkami')

    class Meta:
        ordering = ['-created_at']

    def __str__(self):
        return self.name

    def total_downloads(self):
        return sum(f.download_count for f in self.files.all())

    def vote_score(self):
        ups   = self.votes.filter(vote_type='up').count()
        downs = self.votes.filter(vote_type='down').count()
        return ups - downs

    def upvotes(self):
        return self.votes.filter(vote_type='up').count()

    def downvotes(self):
        return self.votes.filter(vote_type='down').count()

    def get_tags(self):
        return [t.strip() for t in self.tags.split(',') if t.strip()]


class RepositoryFile(models.Model):
    repo           = models.ForeignKey(Repository, on_delete=models.CASCADE, related_name='files')
    file           = models.FileField(upload_to='repo_files/')
    filename       = models.CharField(max_length=255)
    size_bytes     = models.BigIntegerField(default=0)
    download_count = models.IntegerField(default=0)
    uploaded_at    = models.DateTimeField(default=timezone.now)

    def __str__(self):
        return self.filename

    def size_human(self):
        size = self.size_bytes
        for unit in ['B', 'KB', 'MB', 'GB']:
            if size < 1024:
                return f'{size:.1f} {unit}'
            size /= 1024
        return f'{size:.1f} TB'


class Comment(models.Model):
    repo       = models.ForeignKey(Repository, on_delete=models.CASCADE, related_name='comments')
    author     = models.ForeignKey(User, on_delete=models.CASCADE, related_name='comments')
    content    = models.TextField()
    created_at = models.DateTimeField(default=timezone.now)

    class Meta:
        ordering = ['created_at']

    def __str__(self):
        return f'{self.author.username}: {self.content[:50]}'


class Vote(models.Model):
    VOTE_TYPES = [('up', 'Pozytywny'), ('down', 'Negatywny')]
    repo      = models.ForeignKey(Repository, on_delete=models.CASCADE, related_name='votes')
    user      = models.ForeignKey(User, on_delete=models.CASCADE, related_name='votes')
    vote_type = models.CharField(max_length=4, choices=VOTE_TYPES)

    class Meta:
        unique_together = ('repo', 'user')

    def __str__(self):
        return f'{self.user.username} {self.vote_type} {self.repo.name}'


class Repost(models.Model):
    repo        = models.ForeignKey(Repository, on_delete=models.CASCADE, related_name='reposts')
    user        = models.ForeignKey(User, on_delete=models.CASCADE, related_name='reposts')
    reposted_at = models.DateTimeField(default=timezone.now)
    note        = models.CharField(max_length=300, blank=True, default='')

    class Meta:
        unique_together = ('repo', 'user')
        ordering = ['-reposted_at']

    def __str__(self):
        return f'{self.user.username} repostował {self.repo.name}'
