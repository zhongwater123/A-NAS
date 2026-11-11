#!/usr/bin/env bash
# a-nas-check: is the installed A-NAS working (ADR 0034)? It changes nothing.
# Each check prints PASS or FAIL with what to do; the exit status is 1 when
# any failed. Run it after an upgrade or rollback: a release that fails it is
# rolled back (docs/runbooks/deploy-a-nas-packages.md). A service that
# restarted a moment ago gets up to 30 s to answer.
#
# Usage (as root): a-nas-check
set -uo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "run as root: the Host Agent's capabilities and journal are root's" >&2
  exit 1
fi
failures=0
pass() { echo "PASS $*"; }
fail() {
  echo "FAIL $*"
  failures=$((failures + 1))
}

# The product and the Debian packages it runs on are one release (ADR 0031).
product=$(dpkg-query --show --showformat='${db:Status-Status} ${Version}' a-nas 2>/dev/null)
host=$(dpkg-query --show --showformat='${db:Status-Status} ${Version}' a-nas-host 2>/dev/null)
# a-nas's postinst runs this check while dpkg still calls a-nas half-configured.
if [[ "${DPKG_MAINTSCRIPT_PACKAGE:-}" == a-nas && "$product" == half-configured\ * ]]; then
  product="installed ${product#half-configured }"
fi
if [[ "$product" == installed\ * && "$host" == "$product" ]]; then
  pass "a-nas and a-nas-host ${product#installed }"
else
  fail "a-nas is '${product:-missing}' and a-nas-host '${host:-missing}': install both at one version"
fi

# Accounts that own data keep fixed IDs (ADR 0011); the others cannot log in.
for account in a-nas-photos:31000 a-nas-shares:31001 a-nas-family:31002; do
  name=${account%%:*} id=${account#*:}
  if [[ "$(id -u "$name" 2>/dev/null):$(id -g "$name" 2>/dev/null)" == "$id:$id" ]]; then
    pass "$name is $id:$id"
  else
    fail "$name is not $id:$id; A-NAS never adopts an account it did not create: rename the other account"
  fi
done
for name in a-nas-kiosk a-nas-search anas-container; do
  if [[ "$(getent passwd "$name" | cut -d: -f6-7)" == /nonexistent:/usr/sbin/nologin ]]; then
    pass "$name cannot log in"
  else
    fail "$name is missing or has a home or shell A-NAS did not give it"
  fi
done

# A unit of the same name in /etc, as an installation before the packages
# wrote them, overrides the package's.
leftovers=
while read -r unit; do
  [[ -e "/etc/systemd/system/${unit##*/}" ]] && leftovers+="${unit##*/} "
done < <(dpkg-query --listfiles a-nas 2>/dev/null | grep -E '^/usr/lib/systemd/system/[^/]+\.(service|socket|slice|path)$')
if [[ -z "$leftovers" ]]; then
  pass "no unit in /etc/systemd/system overrides the package's"
else
  fail "these units in /etc/systemd/system override the package's: $leftovers"
fi

if grep -Eq '^ANAS_SMB_INTERFACE=[A-Za-z0-9_.:-]+$' /etc/a-nas/site.conf 2>/dev/null; then
  pass "site.conf names the SMB interface"
else
  fail "set ANAS_SMB_INTERFACE in /etc/a-nas/site.conf, then run a-nas-site"
fi
if systemctl is-active --quiet anas-site.service; then
  pass "anas-site.service applied this machine's settings"
else
  fail "anas-site.service failed; see journalctl -u anas-site"
fi

for unit in anas-host-agent.service anas-api.service anas-photos.service anas-shares.service anas-family.service \
  anas-ai.socket anas-ffmpeg.socket anas-vectors-proxy@a-nas-photos.socket smbd.service; do
  # A unit that is starting or restarting, as smbd does when the Host Agent
  # writes Samba's configuration, gets time to finish.
  for _ in $(seq 1 30); do
    case "$(systemctl is-active "$unit")" in activating | reloading | deactivating) sleep 1 ;; *) break ;; esac
  done
  if systemctl is-active --quiet "$unit"; then
    pass "$unit is active"
  else
    fail "$unit is $(systemctl is-active "$unit"); see journalctl -u $unit"
  fi
done
# Stopped is a valid state for web search: the administrator turns it off.
echo "INFO web search: $(systemctl is-active anas-search.service)"

# Web files and the terminal run as the signed-in user, so the Host Agent must
# keep CAP_SETUID and CAP_SETGID in its sandbox and prove the switch at
# startup (issue #38).
since=$(systemctl show -p ActiveEnterTimestamp --value anas-host-agent.service)
identity_switch=
for _ in $(seq 1 30); do
  identity_switch=$(journalctl -u anas-host-agent.service --since "${since:-today}" -o cat --no-pager 2>/dev/null |
    grep -Eo 'file broker (identity switch verified|cannot switch to user identities)' | tail -n 1)
  [[ -n "$identity_switch" ]] && break
  sleep 1
done
pid=$(systemctl show -p MainPID --value anas-host-agent.service)
cap_eff=$(awk '/^CapEff:/ {print $2}' "/proc/$pid/status" 2>/dev/null)
if [[ "$identity_switch" == "file broker identity switch verified" && -n "$cap_eff" ]] && (((16#$cap_eff >> 6 & 3) == 3)); then
  pass "the Host Agent starts file workers as users"
else
  fail "the Host Agent cannot start file workers as users (CapEff=${cap_eff:-unknown}; ${identity_switch:-no identity probe result}): roll back this release"
fi

if python3 -I - <<'PY'; then
import json
import time
import urllib.request

for _ in range(30):
    try:
        with urllib.request.urlopen("http://127.0.0.1:8080/healthz", timeout=3) as response:
            if response.status == 200 and json.load(response).get("status") == "ok":
                raise SystemExit(0)
    except (OSError, ValueError):
        pass
    time.sleep(1)
raise SystemExit(1)
PY
  pass "the Product Service answers /healthz"
else
  fail "the Product Service does not answer http://127.0.0.1:8080/healthz; see journalctl -u anas-api"
fi

if ((failures > 0)); then
  echo "a-nas-check: $failures checks failed"
  exit 1
fi
echo "a-nas-check: all checks passed"
