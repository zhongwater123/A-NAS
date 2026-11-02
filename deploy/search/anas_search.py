"""SearXNG for A-NAS web search (ADR 0030), unmodified behind this WSGI entry.

SearXNG expects a reverse proxy on a trusted address (127.0.0.0/8 by default)
that names the client in X-Real-IP or X-Forwarded-For, and logs an error when
it finds neither (searx/botdetection/trusted_proxies.py). Requests reach
Granian over a Unix socket instead, with no address at all, and the only
client is the Product Service on this machine. So the entry stands in for that
proxy and names 127.0.0.1 for both; SearXNG's limiter, which would act on the
client's address, is off.
"""

from searx.webapp import app


def application(environ, start_response):
    environ["REMOTE_ADDR"] = "127.0.0.1"
    environ["HTTP_X_REAL_IP"] = "127.0.0.1"
    environ.pop("HTTP_X_FORWARDED_FOR", None)
    return app(environ, start_response)
