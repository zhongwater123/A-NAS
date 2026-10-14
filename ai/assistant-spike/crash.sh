#!/usr/bin/env bash
# Kills a member's assistant and checks that systemd notices despite PrivatePIDs.
main=$(systemctl show -p MainPID --value anas-assistant@bob.service)
echo "bob main pid: $main ($(cat /proc/$main/comm 2>/dev/null))"
kill -9 "$main"
for i in $(seq 1 20); do
  s=$(systemctl show -p ActiveState -p SubState -p Result --value anas-assistant@bob.service | tr '\n' ' ')
  case "$s" in active\ running*) sleep 0.5;; *) echo "after ~$((i/2)) s: $s"; break;; esac
done
systemctl show -p ActiveState -p SubState -p Result anas-assistant@bob.service | tr '\n' ' '; echo
pgrep -u bob -a | head -5 || echo "no bob processes left"
