#!/usr/bin/env bash
# a-nas-site: applies /etc/a-nas/site.conf and /etc/a-nas/internet.env now,
# as a reboot would (ADR 0034). The a-nas package runs it after installing.
#
# It reloads systemd, so that a-nas-generator writes the drop-ins again; runs
# anas-site.service again; restarts the services that read site.conf; and
# starts or stops the internet entry's firewall and tunnel. Then it runs
# a-nas-check. Restarting the Product Service and the Host Agent interrupts
# what they are doing for a few seconds.
#
# Usage (as root): a-nas-site
set -uo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
status=0
systemctl daemon-reload
systemctl restart anas-site.service || status=1
# smbd waits for its interface again only when it starts; a running smbd keeps
# its listeners, and the Host Agent restarts it when Samba's configuration
# listens elsewhere.
systemctl restart anas-host-agent.service anas-api.service || status=1
systemctl try-restart 'anas-kiosk@*.service' || status=1
for unit in anas-internet-firewall.service wg-quick@wg-anas.service; do
  # a-nas-generator wants them while internet.env turns the entry on.
  if [[ -e "/run/systemd/generator/multi-user.target.wants/$unit" ]]; then
    # A tunnel that does not come up leaves only the internet entry
    # unreachable; a firewall table that does not load is a failure.
    if ! systemctl start "$unit"; then
      echo "$unit did not start; see journalctl -u $unit (docs/runbooks/enable-internet-access.md)" >&2
      [[ "$unit" == wg-quick@* ]] || status=1
    fi
  elif systemctl is-active --quiet "$unit"; then
    systemctl stop "$unit" || status=1
  fi
done
/usr/sbin/a-nas-check || status=1
exit "$status"
