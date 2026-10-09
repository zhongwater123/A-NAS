"""A-NAS AI Worker: turns images into vectors for the photo service.

See docs/architecture/photo-ai.md. The Worker never sees paths on the data
volume: the photo service passes each image as a read-only descriptor.
"""
