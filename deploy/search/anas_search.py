"""SearXNG for A-NAS web search (ADR 0030), unmodified behind this WSGI entry.

Requests reach Granian over a Unix socket, so the WSGI environment has no
REMOTE_ADDR, and SearXNG's bot detection would log an error for every
request before it falls back to a black-hole address
(searx/botdetection/trusted_proxies.py). The only client is the Product
Service on this machine, so the entry names it 127.0.0.1.
"""

from searx.webapp import app


def application(environ, start_response):
    if not environ.get("REMOTE_ADDR"):
        environ["REMOTE_ADDR"] = "127.0.0.1"
    return app(environ, start_response)
