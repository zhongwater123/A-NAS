#!/usr/bin/env bash
# ADR 0034 step 3: builds the device image with mkosi and checks, in QEMU and
# with Debian 13's systemd 257, systemd-boot's boot assessment, A/B updates by
# systemd-sysupdate, power loss during an update, a models component and the
# product as a system extension. What it found:
# docs/research/sysupdate-image-verification.md.
#
# Usage, on Linux with Docker and /dev/kvm (WSL works):
#   WORK=/mnt/d/sysupdate-verify.img deploy/sysupdate-spike/verify.sh [STEP...]
# Steps: keys build run clean (the default is all four). "clean" copies
# results.log and the logs to WORK-results/ next to WORK, then removes the
# container, the builder image and WORK.
#
# WORK holds the package cache, the images the update server offers, test
# keys and logs: about 8 GB. A path ending in .img is an ext4 image file that
# the container mounts instead of a directory: on WSL, put it on a Windows
# drive, because WSL's own disk never shrinks. The default is
# ~/sysupdate-verify. mkosi's workspace and the VM's disks (up to about 8 GB)
# stay on the container's own disk and are removed after each build and run.
#
# Everything runs as root in a privileged container of its own; the test
# keys (GPG, CA, SSH) are made for the run and are not release keys.
set -euo pipefail

container=anas-sysupdate-verify
builder=anas-sysupdate-builder:trixie

if [[ ${1:-} != --inside ]]; then
	spike=$(cd "$(dirname "$0")" && pwd)
	work=${WORK:-$HOME/sysupdate-verify}
	if [[ ! -f $spike/mkosi.conf || $work != /?* || $work == "$HOME" ]]; then
		echo "verify.sh: cannot find the spike, or WORK is not an absolute path of its own" >&2
		exit 2
	fi
	results=${work%.img}-results
	if (($# == 0)); then set -- keys build run clean; fi
	if ! docker image inspect "$builder" >/dev/null 2>&1; then
		# Debian 13's tools; the base is the system test's image, which
		# scripts/build-system-test-image.sh makes without Docker Hub.
		docker build -q -t "$builder" - >/dev/null <<-'DOCKERFILE'
			FROM anas-systemd:trixie
			RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
			    mkosi systemd-boot systemd-boot-efi systemd-ukify systemd-repart systemd-container \
			    qemu-system-x86 ovmf erofs-utils dosfstools mtools gnupg openssl python3 \
			    xz-utils zstd cpio kmod debian-archive-keyring openssh-client e2fsprogs fdisk \
			    apt dpkg man-db less >/dev/null && rm -rf /var/cache/apt/archives/*.deb
		DOCKERFILE
	fi
	if [[ -z $(docker ps -q --filter "name=^${container}$") ]]; then
		docker rm -f "$container" >/dev/null 2>&1 || true
		if [[ $work == *.img ]]; then
			if [[ ! -f $work ]]; then
				docker run --rm -v "$(dirname "$work"):/store" "$builder" \
					bash -c 'truncate -s 48G "/store/$1" && mkfs.ext4 -q "/store/$1"' _ "$(basename "$work")"
			fi
			docker run -d --name "$container" --privileged -v "$spike:/spike:ro" -v "$(dirname "$work"):/store" \
				"$builder" bash -c 'mkdir -p /work && mount -o loop "/store/$1" /work && exec sleep infinity' _ "$(basename "$work")" >/dev/null
		else
			mkdir -p "$work"
			docker run -d --name "$container" --privileged -v "$spike:/spike:ro" -v "$work:/work" \
				"$builder" sleep infinity >/dev/null
		fi
	fi
	for step; do
		case $step in
		clean)
			mkdir -p "$results"
			# Through the container: docker cp does not see an image it mounted.
			docker exec "$container" tar -C /work -cf - results.log logs | tar -C "$results" -xf -
			docker rm -f "$container" >/dev/null
			docker rmi "$builder" >/dev/null
			rm -rf -- "$work"
			echo "results in $results"
			;;
		*)
			# A copy, so that editing the checkout cannot change a running step.
			docker exec "$container" cp /spike/verify.sh /work/.verify.sh
			docker exec "$container" bash /work/.verify.sh --inside "$step"
			;;
		esac
	done
	exit 0
fi

# ---- inside the container ------------------------------------------------

W=/work
SU=/usr/lib/systemd/systemd-sysupdate
# The VM's disks and mkosi's workspace stay on the container's own (fast)
# disk and are removed after each run and build; WORK may be slow.
VM=/var/tmp/a-nas-vm
export GNUPGHOME=$W/keys/gnupg
mkdir -p "$W/logs"

note() { printf '%s %s\n' "$(date +%T)" "$*" | tee -a "$W/results.log"; }
check() { # check NAME COMMAND...: records PASS or FAIL
	local name=$1
	shift
	if "$@"; then note "PASS $name"; else note "FAIL $name"; fi
}
size() { stat -c %s "$1"; }
mib() { awk -v b="$1" 'BEGIN { printf "%.1f MiB", b / 1048576 }'; }

step_keys() {
	local gen=$W/gen-extra
	mkdir -p "$W/keys" "$gen/usr/lib/systemd" "$gen/usr/local/share/ca-certificates" "$gen/root/.ssh"
	if [[ ! -d $GNUPGHOME ]]; then
		mkdir -m 0700 "$GNUPGHOME"
		gpg --batch --passphrase '' --quick-gen-key 'A-NAS sysupdate verification (test only)' ed25519 sign never 2>/dev/null
	fi
	gpg --export >"$gen/usr/lib/systemd/import-pubring.gpg"
	if [[ ! -f $W/keys/server.crt ]]; then
		openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 30 \
			-subj '/CN=A-NAS sysupdate test CA' -keyout "$W/keys/ca.key" -out "$W/keys/ca.crt" 2>/dev/null
		openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj '/CN=10.0.2.2' \
			-keyout "$W/keys/server.key" -out "$W/keys/server.csr" 2>/dev/null
		openssl x509 -req -in "$W/keys/server.csr" -CA "$W/keys/ca.crt" -CAkey "$W/keys/ca.key" \
			-days 30 -extfile <(printf 'subjectAltName=IP:10.0.2.2\n') -out "$W/keys/server.crt" 2>/dev/null
	fi
	cp "$W/keys/ca.crt" "$gen/usr/local/share/ca-certificates/a-nas-test-ca.crt"
	[[ -f $W/keys/ssh ]] || ssh-keygen -q -t ed25519 -N '' -f "$W/keys/ssh"
	chmod 0700 "$gen/root" "$gen/root/.ssh"
	install -m 0600 "$W/keys/ssh.pub" "$gen/root/.ssh/authorized_keys"
	note "keys ready (test only)"
}

build_os() { # build_os VERSION VARIANT [KERNEL-COMMAND-LINE...]
	local version=$1 variant=$2 start
	shift 2
	local extra=()
	for arg; do extra+=("--kernel-command-line=$arg"); done
	start=$(date +%s)
	mkosi --directory="$W/config" --image-version="$version" --environment="ANAS_VARIANT=$variant" \
		--extra-tree="$W/gen-extra" --output-directory="$W/out/$version" \
		--cache-directory="$W/cache" --package-cache-directory="$W/pkgcache" \
		--workspace-directory=/var/tmp "${extra[@]}" -f build >"$W/logs/build-$version.log" 2>&1 ||
		{
			tail -n 40 "$W/logs/build-$version.log"
			return 1
		}
	local out=$W/out/$version/a-nas_$version
	xz -T2 -6 -k -f "$out.usr.raw"
	note "built OS $version ($variant) in $(($(date +%s) - start)) s:" \
		"/usr image $(mib "$(du -B1 "$out.usr.raw" | cut -f1)") in a $(mib "$(size "$out.usr.raw")") slot," \
		"xz $(mib "$(size "$out.usr.raw.xz")"), UKI $(mib "$(size "$out.efi")")"
	# Kept: what the update server offers, and version 1's disk to install.
	find "$W/out/$version" -mindepth 1 ! -name "a-nas_$version.usr.raw.xz" ! -name "a-nas_$version.efi" \
		! -name a-nas_1.raw -delete
}

build_product() { # build_product VERSION: the demo package, then a sysext from it
	local version=$1
	local dir=$W/product/$version
	rm -rf -- "$dir"
	mkdir -p "$dir"
	cp -r /spike/product "$dir/pkg"
	find "$dir/pkg" -type f -exec sed -i "s/@VERSION@/$version/" {} +
	find "$dir/pkg" -type d -exec chmod 0755 {} +
	find "$dir/pkg" -type f -exec chmod 0644 {} +
	chmod 0755 "$dir/pkg/DEBIAN/postinst" "$dir/pkg/usr/lib/a-nas-demo/run.sh"
	mkdir -p "$dir/pkg/usr/lib/systemd/system/multi-user.target.wants"
	ln -s ../a-nas-demo.service "$dir/pkg/usr/lib/systemd/system/multi-user.target.wants/a-nas-demo.service"
	dpkg-deb --root-owner-group -Zxz --build "$dir/pkg" "$dir/a-nas-demo_$version.deb" >/dev/null
	# What mkosi's Format=sysext would keep: /usr and /opt, no maintainer scripts.
	dpkg-deb -x "$dir/a-nas-demo_$version.deb" "$dir/tree"
	note "product $version: left out of the sysext: $(cd "$dir/tree" && find . -mindepth 1 -maxdepth 1 ! -name usr ! -name opt -printf '%P ')" \
		"and the maintainer scripts $(dpkg-deb --ctrl-tarfile "$dir/a-nas-demo_$version.deb" | tar -t | grep -v -e '^\./$' -e control -e md5sums | tr '\n' ' ')"
	find "$dir/tree" -mindepth 1 -maxdepth 1 ! -name usr ! -name opt -exec rm -rf -- {} +
	mkdir -p "$dir/tree/usr/lib/extension-release.d"
	printf 'ID=debian\nVERSION_ID=13\nEXTENSION_RELOAD_MANAGER=1\n' >"$dir/tree/usr/lib/extension-release.d/extension-release.a-nas-product"
	mkfs.erofs -zlz4hc "$dir/a-nas-product_$version.raw" "$dir/tree" >/dev/null
	note "built product sysext $version: $(size "$dir/a-nas-product_$version.raw") bytes"
}

build_models() { # build_models VERSION: 100 MiB that does not compress, like model weights
	local version=$1
	local dir=$W/models/$version
	rm -rf -- "$dir"
	mkdir -p "$dir/tree"
	head -c 100M /dev/urandom >"$dir/tree/weights.bin"
	echo "$version" >"$dir/tree/VERSION"
	tar -C "$dir/tree" -cf "$dir/a-nas-models_$version.tar" .
	note "built models $version: $(mib "$(size "$dir/a-nas-models_$version.tar")")"
}

prepare_config() {
	[[ -f /spike/mkosi.conf ]] || exit 2
	rm -rf -- "$W/config"
	cp -r /spike "$W/config"
	mkdir -p "$W/cache" "$W/pkgcache"
	# A Windows checkout shows every file as executable; set the modes the
	# image needs.
	find "$W/config" -type d -exec chmod 0755 {} +
	find "$W/config" -type f -exec chmod 0644 {} +
	chmod 0755 "$W/config/mkosi.postinst" "$W/config/mkosi.extra/usr/lib/a-nas/health-check.sh"
}

step_build() {
	prepare_config
	# A version already in WORK/out is kept; "verify.sh os ..." rebuilds one.
	local version variant
	for version in 1 2 3 4 5; do
		[[ ! -f $W/out/$version/a-nas_$version.usr.raw.xz ]] || continue
		variant=good
		if ((version == 3)); then variant=health-broken; fi
		# Version 4's kernel cannot start: no init in the initrd, so it
		# panics and panic=10 reboots it.
		if ((version == 4)); then build_os 4 good rdinit=/nonexistent; else build_os "$version" "$variant"; fi
	done
	build_product 1
	build_product 2
	build_models 1
	build_models 2
}

# ---- the update server and the VM -----------------------------------------

publish() { # publish DIR FILE...: adds the files and signs a new SHA256SUMS
	local dir=$W/srv/$1
	shift
	mkdir -p "$dir"
	cp -f "$@" "$dir/"
	(cd "$dir" && find . -maxdepth 1 -type f ! -name 'SHA256SUMS*' -printf '%P\n' | sort | xargs sha256sum >SHA256SUMS)
	gpg --batch --yes --detach-sign -o "$dir/SHA256SUMS.gpg" "$dir/SHA256SUMS"
}
publish_os() { publish os "$W/out/$1/a-nas_$1.usr.raw.xz" "$W/out/$1/a-nas_$1.efi"; }

http_mark() { wc -l <"$W/logs/http.log"; }
http_since() { # http_since MARK: bytes sent after MARK, and to which files
	tail -n +"$(($1 + 1))" "$W/logs/http.log" | awk '$2 == "end" { total += $4; files = files " " $3 "=" $4 }
		END { printf "%d bytes (%.1f MiB):%s", total, total / 1048576, files }'
}
http_wait() { # http_wait EVENT PATH-SUBSTRING MARK: until that event shows up after MARK (600 s at most)
	local deadline=$((SECONDS + 600))
	until tail -n +"$(($3 + 1))" "$W/logs/http.log" | awk -v e="$1" -v p="$2" '$2 == e && index($3, p) { found = 1 } END { exit !found }'; do
		if ((SECONDS > deadline)); then
			note "no $1 of $2 within 600 s"
			return 1
		fi
		sleep 0.1
	done
}

g() {
	ssh -q -i "$W/keys/ssh" -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
		-o ConnectTimeout=5 -o ServerAliveInterval=5 root@127.0.0.1 "$@"
}

boots=0
vm=
boot() { # starts the VM; 0 once SSH answers, 1 if the VM went down first
	boots=$((boots + 1))
	qemu-system-x86_64 -machine q35,accel=kvm -cpu host -smp 2 -m 1536 -no-reboot -nodefaults \
		-display none -serial "file:$W/logs/boot-$boots.log" \
		-drive if=pflash,format=raw,unit=0,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
		-drive if=pflash,format=raw,unit=1,file="$VM/vars.fd" \
		-drive if=virtio,format=raw,file="$VM/disk.raw" \
		-drive if=virtio,format=raw,file="$VM/data.raw" \
		-netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net-pci,netdev=n0 \
		-device virtio-rng-pci &
	vm=$!
	echo "$vm" >"$W/vm.pid"
	local deadline=$((SECONDS + 300))
	while ((SECONDS < deadline)); do
		if ! kill -0 "$vm" 2>/dev/null; then
			wait "$vm" || true
			return 1
		fi
		if g true 2>/dev/null; then
			# SSH can answer before a failing health check reboots the VM.
			g systemctl is-system-running --wait >/dev/null || true
			sleep 3
			if kill -0 "$vm" 2>/dev/null; then return 0; fi
			wait "$vm" || true
			return 1
		fi
		sleep 2
	done
	note "boot $boots: no SSH after 300 s"
	kill -9 "$vm"
	wait "$vm" || true
	return 1
}
tried() { grep -ao 'mount.usr=PARTLABEL=a-nas_[0-9]*' "$W/logs/boot-$1.log" | head -n 1 | sed 's/.*_/v/'; }
boot_until_up() { # boot_until_up MAX: boots until SSH answers; reports each try
	local first=$((boots + 1)) n
	for ((n = 0; n < $1; n++)); do
		if boot; then break; fi
	done
	local tries=""
	for ((n = first; n <= boots; n++)); do tries+=" $(tried "$n")"; done
	note "boots $first-$boots tried:$tries"
}
shutdown_vm() {
	g systemctl reboot || true
	wait "$vm" || true
}
power_cut() {
	kill -9 "$vm"
	wait "$vm" || true
}
# The guest expands $IMAGE_VERSION.
# shellcheck disable=SC2016
booted() { g '. /etc/os-release && echo "$IMAGE_VERSION"'; }
esp() { g 'ls -A /efi/EFI/Linux | tr "\n" " "'; }
slots() { g 'lsblk -n -o PARTLABEL /dev/vda | tr "\n" " "'; }
manifest() { g 'find /var/lib/a-nas/data /srv/a-nas /etc/a-nas/site.conf -type f -print0 | sort -z | xargs -0 sha256sum'; }
timed_update() { # timed_update LABEL [ARGS...]: runs systemd-sysupdate update, notes time and download
	local label=$1 mark start
	shift
	mark=$(http_mark)
	start=$(date +%s.%N)
	g "$SU" "$@" update >"$W/logs/update-$label.log" 2>&1 || note "update $label exited with $?"
	note "update $label: $(awk -v s="$start" -v e="$(date +%s.%N)" 'BEGIN { printf "%.1f s", e - s }'), downloaded $(http_since "$mark")"
}

step_run() {
	local pid
	for pid in vm serve; do
		if [[ -f $W/$pid.pid ]]; then kill -9 "$(cat "$W/$pid.pid")" 2>/dev/null || true; fi
	done
	rm -rf -- "$W/srv" "$VM"
	mkdir -p "$W/srv" "$VM"
	: >"$W/logs/http.log"
	: >"$W/rate"
	python3 /spike/serve.py "$W/srv" 8443 "$W/keys/server.crt" "$W/keys/server.key" \
		"$W/logs/http.log" "$W/rate" >"$W/logs/serve.log" 2>&1 &
	echo $! >"$W/serve.pid"
	cp --sparse=always "$W/out/1/a-nas_1.raw" "$VM/disk.raw"
	cp /usr/share/OVMF/OVMF_VARS_4M.fd "$VM/vars.fd"
	truncate -s 2G "$VM/data.raw"
	mkfs.ext4 -q -L anas-data "$VM/data.raw"
	note "disk: $(sfdisk -l -o Device,Size,Name "$VM/disk.raw" | tail -n 4 | tr -s ' ' | tr '\n' ';')"

	# Version 1 as installed, with state on the persistent root and the data volume.
	boot_until_up 1
	note "booted v$(booted); ESP: $(esp); slots: $(slots); $(g systemd-analyze | head -n 1)"
	g 'mkdir -p /var/lib/a-nas/data /etc/a-nas && head -c 64M /dev/urandom >/var/lib/a-nas/data/blob &&
		echo "rows=1" >/var/lib/a-nas/data/state.db && echo "smb_interface=enp0s2" >/etc/a-nas/site.conf &&
		head -c 32M /dev/urandom >/srv/a-nas/photo.jpg && sync'
	manifest >"$W/logs/manifest-0"

	# 6: the product as a system extension, installed by its own component.
	publish product "$W/product/1/a-nas-product_1.raw"
	publish models "$W/models/1/a-nas-models_1.tar"
	timed_update product-1 --component=product
	timed_update models-1 --component=models
	g 'ls -l /var/lib/extensions /var/lib/a-nas-sysext /var/lib/a-nas/models' >"$W/logs/components-1.log"
	local helper
	# mkosi enabled it in /etc with systemctl preset-all (Debian enables every
	# unit with [Install]); the first boot goes without it.
	g systemctl disable a-nas-sysext-ready.service 2>/dev/null
	for helper in without with; do
		shutdown_vm
		boot_until_up 1
		note "product after boot $helper a-nas-sysext-ready: sysext: $(g 'systemd-sysext list --no-legend | tr -s " " | tr "\n" ";"')" \
			"a-nas-demo $(g systemctl is-active a-nas-demo.service);" \
			"user a-nas-demo: $(g getent passwd a-nas-demo || echo missing);" \
			"postinst user: $(g getent passwd a-nas-demo-postinst || echo missing);" \
			"/etc/a-nas/demo.conf: $(g cat /etc/a-nas/demo.conf || echo missing)"
		g 'journalctl -b -o short-monotonic -u systemd-sysext -u systemd-sysusers -u a-nas-sysext-ready -u systemd-tmpfiles-setup -u a-nas-demo' \
			>"$W/logs/product-boot-$helper.log" || true
		g systemctl enable a-nas-sysext-ready.service 2>/dev/null
	done
	check "6 product service starts at boot with the helper" test "$(g systemctl is-active a-nas-demo.service)" = active
	# What the /usr transfer would do with Path=auto while the sysext is merged.
	g 'mkdir -p /run/auto.d && sed "s|^Path=/run/a-nas/system-disk|Path=auto|" /usr/lib/sysupdate.d/50-usr.transfer >/run/auto.d/50-usr.transfer &&
		cp /usr/lib/sysupdate.d/60-uki.transfer /run/auto.d/'
	note "Path=auto under a merged sysext: $(g "$SU" --definitions=/run/auto.d list 2>&1 | grep -v 'executable' | tail -n 1)"
	note "Path via a-nas-system-disk: $(g readlink /run/a-nas/system-disk)"

	# 1 and 4: v1 -> v2, blessed once the health check passed; state kept.
	publish_os 2
	timed_update os-2
	note "after update to 2: ESP: $(esp); slots: $(slots)"
	shutdown_vm
	boot_until_up 1
	note "booted v$(booted); bless-boot $(g systemctl is-active systemd-bless-boot.service); health $(g systemctl is-active a-nas-health.service); ESP: $(esp); $(g systemd-analyze | head -n 1)"
	check "1 v2 boots and is blessed" test "$(booted)/$(esp)" = "2/a-nas_1.efi a-nas_2.efi "
	check "4 state kept on v1 -> v2" cmp -s <(manifest) "$W/logs/manifest-0"
	note "v2 additions: user a-nas-v2: $(g getent passwd a-nas-v2 || echo missing);" \
		"unit enabled in /usr: $(g systemctl is-active a-nas-v2-usr.service); unit enabled in /etc: $(g systemctl is-active a-nas-v2-etc.service)"
	note "product on v2: a-nas-demo $(g systemctl is-active a-nas-demo.service)"

	# 5: nothing new, then only the models change.
	timed_update os-none
	timed_update models-same --component=models
	publish models "$W/models/2/a-nas-models_2.tar"
	timed_update os-models-changed
	timed_update models-2 --component=models
	note "models: $(g 'readlink /var/lib/a-nas/models/current; ls /var/lib/a-nas/models | tr "\n" " "')"

	# 2a: v3's health check fails; systemd-boot must come back to v2.
	publish_os 3
	timed_update os-3
	shutdown_vm
	boot_until_up 6
	note "after v3: booted v$(booted); ESP: $(esp); slots: $(slots)"
	check "2 health-broken v3 falls back to v2" test "$(booted)" = 2
	check "4 state kept after v3" cmp -s <(manifest) "$W/logs/manifest-0"
	g "$SU" list >"$W/logs/list-after-3.log" 2>&1 || true
	timed_update os-3-again
	# Does the reboot timer's command reboot into the version that failed?
	g "$SU" reboot >"$W/logs/reboot-after-3.log" 2>&1 || true
	sleep 10
	if kill -0 "$vm" 2>/dev/null; then note "systemd-sysupdate reboot after v3: no reboot"; else
		wait "$vm" || true
		note "systemd-sysupdate reboot after v3: rebooted"
		boot_until_up 6
	fi

	# 2b: v4's kernel panics; the same.
	publish_os 4
	timed_update os-4
	shutdown_vm
	boot_until_up 6
	note "after v4: booted v$(booted); ESP: $(esp); slots: $(slots)"
	check "2 panicking v4 falls back to v2" test "$(booted)" = 2

	# 3: power cut while systemd-sysupdate writes v5.
	publish_os 5
	local point mark job
	for point in usr-mid uki-mid uki-done; do
		case $point in
		usr-mid) printf 'usr.raw 20000000\n' >"$W/rate" ;;
		uki-*) printf '.efi 5000000\n' >"$W/rate" ;;
		esac
		mark=$(http_mark)
		g "$SU" update >"$W/logs/update-cut-$point.log" 2>&1 &
		job=$!
		case $point in
		usr-mid) http_wait start a-nas_5.usr "$mark" && sleep 5 ;;
		uki-mid) http_wait start a-nas_5.efi "$mark" && sleep 2 ;;
		uki-done) http_wait end a-nas_5.efi "$mark" || true ;;
		esac
		power_cut
		wait "$job" || true
		boot_until_up 6
		note "power cut at $point: booted v$(booted); ESP: $(esp); slots: $(slots);" \
			"kernel on the file systems: $(g 'journalctl -b -k -o cat | grep -E "EXT4-fs \(vda4\)|FAT-fs" | tr "\n" ";"' || true)"
		# A cut after the install step may already have switched to v5.
		check "3 boots after a power cut at $point" grep -qx '[25]' <<<"$(booted)"
		check "4 state kept after a power cut at $point" cmp -s <(manifest) "$W/logs/manifest-0"
		g "$SU" list >"$W/logs/list-cut-$point.log" 2>&1 || true
		: >"$W/rate"
	done
	timed_update os-5
	shutdown_vm
	boot_until_up 1
	note "booted v$(booted); ESP: $(esp)"
	check "3 v5 installs after the power cuts" test "$(booted)/$(esp)" = "5/a-nas_2.efi a-nas_5.efi "
	check "4 state kept on v5" cmp -s <(manifest) "$W/logs/manifest-0"

	# 6: update the product without a reboot.
	publish product "$W/product/2/a-nas-product_2.raw"
	timed_update product-2 --component=product
	g 'systemd-sysext refresh && systemctl restart a-nas-demo.service' || note "refresh or restart failed"
	sleep 2
	note "product refreshed: running $(g cat /var/lib/a-nas-demo/running-version || echo nothing); sysext: $(g 'systemd-sysext list --no-legend | tr -s " " | tr "\n" ";"')"
	check "6 product updated without a reboot" test "$(g cat /var/lib/a-nas-demo/running-version)" = 2
	shutdown_vm
	note "done after $boots boots"
	kill "$(cat "$W/serve.pid")" 2>/dev/null || true
	rm -rf -- "$VM" "$W/srv"
}

case $2 in
keys) step_keys ;;
build) step_build ;;
run) step_run ;;
# Rebuilds one OS version, e.g. "verify.sh os 3 health-broken".
os)
	prepare_config
	build_os "${@:3}"
	;;
*)
	echo "verify.sh: unknown step $2" >&2
	exit 2
	;;
esac
