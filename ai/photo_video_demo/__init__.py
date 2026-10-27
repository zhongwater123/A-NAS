"""Photo-to-video MCP demo for a development machine (docs/specs/photo-to-video.md).

Claude Code runs server.py as a stdio MCP server. It signs in to the product
service as a test account and only relays: search, authorization and ranking
stay in the photo service. Everything but server.py uses the standard library
only, so its tests run in `make ai-test` without the MCP SDK.
"""
