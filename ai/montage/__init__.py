"""Cutting videos for the assistant (docs/specs/photo-to-video.md, #322).

render joins stills or video clips with FFmpeg and needs only the standard
library, so the assistant's MCP server imports it inside the sandbox.
moments ranks video windows against a query and checks an editing plan; it
needs NumPy and runs where the vectors are. look has a local vision model
describe cards and look at a plan's frames before it is cut (#326).
"""
