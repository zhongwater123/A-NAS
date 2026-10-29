#!/usr/bin/env bash
# Kills a member's assistant and checks that systemd notices despite PrivatePIDs.
main=$(systemctl show -p MainPID --value anas-assistant@bob.service)
echo "bob main pid: $main ($(cat "/proc/$main/comm" 2>/dev/null))"
kill -9 "$main"
for i in $(seq 1 20); do
  # Only ActiveState: systemctl does not print several --value properties in the order asked.
  s=$(systemctl show -p ActiveState --value anas-assistant@bob.service)
  [ "$s" = active ] && { sleep 0.5; continue; }
  echo "after ~$((i/2)) s: $s"; break
done
systemctl show -p ActiveState -p SubState -p Result anas-assistant@bob.service | tr '\n' ' '; echo
pgrep -u bob -a | head -5 || echo "no bob processes left"
