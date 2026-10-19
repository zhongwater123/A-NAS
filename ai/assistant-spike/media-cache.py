"""Hermes's own pruning of its image cache, which `hermes serve` does not run.

Hermes keeps every image a tool hands the model (a photo the member asked it to look at,
through look_at_photo) in $HERMES_HOME/cache/images. Its messaging gateway prunes that cache
hourly to a day's worth (gateway/run.py _housekeeping_media_caches); serve has no such chore.
anas-assistant-curator@.service runs this after the weekly curator, as the member, in the
assistant's sandbox.
"""
from agent.provider_media import MEDIA_CACHE_MAX_AGE_HOURS
from gateway.platforms.base import cleanup_image_cache

removed = cleanup_image_cache(max_age_hours=MEDIA_CACHE_MAX_AGE_HOURS)
print(f"image cache: removed {removed} file(s) older than {MEDIA_CACHE_MAX_AGE_HOURS} h")
