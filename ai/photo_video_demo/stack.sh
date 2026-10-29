#!/usr/bin/env bash
# A Fake product service with an AI Worker for the photo-to-video demo, in one
# disposable Debian 13 container; works wherever Docker does (macOS included,
# where anas-api does not build). Nothing here touches a NAS.
#
#   ai/photo_video_demo/stack.sh start        build and start; the API listens on 127.0.0.1:$PORT
#   ai/photo_video_demo/stack.sh seed         create the test account and import generated test photos
#   ai/photo_video_demo/stack.sh import DIR   create the test account if needed and upload DIR's photos and videos
#   ai/photo_video_demo/stack.sh stop         remove the container (the state goes with it)
#
# The test account and password come from ANAS_DEMO_USER and ANAS_DEMO_PASSWORD,
# the same variables the MCP server reads. Without ANAS_DEMO_MODELS the Worker
# is fake: its vectors come from a hash of the input, so search results only
# show that the path works, not how well it ranks. With ANAS_DEMO_MODELS (a
# directory laid out like the development machines' model directory) it loads
# EmbeddingGemma 2 from there, checked against deploy/models/.
set -euo pipefail

NAME=${ANAS_DEMO_STACK:-anas-photo-video-demo}
PORT=${ANAS_DEMO_PORT:-18091}
MODELS=${ANAS_DEMO_MODELS:-}
IMAGE=golang:1.27.3-trixie
ROOT=$(cd "$(dirname "$0")/../.." && pwd)

inside() {
	docker exec -i "$NAME" bash -euo pipefail -s -- "$@"
}

start() {
	local mode=fake mount=()
	# anas-api embeds the desktop and refuses to start without it.
	if [ ! -f "$ROOT/internal/webui/dist/index.html" ]; then
		echo "the web UI is not built: run make web-build first" >&2
		exit 1
	fi
	if [ -n "$MODELS" ]; then
		mode=real
		mount=(-v "$(cd "$MODELS" && pwd):/models:ro")
	fi
	docker rm -f "$NAME" >/dev/null 2>&1 || true
	docker run -d --name "$NAME" -p "127.0.0.1:$PORT:8081" -v "$ROOT:/src:ro" ${mount[@]+"${mount[@]}"} \
		-v anas-demo-gocache:/root/.cache -v anas-demo-gomod:/go/pkg/mod "$IMAGE" sleep infinity >/dev/null
	inside "$mode" <<'EOF'
mode=$1
packages=(ffmpeg python3 curl socat)
# MediaPipe renders through EGL even on the CPU.
[ "$mode" = real ] && packages+=(python3-venv libegl1 libgles2)
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "${packages[@]}" >/dev/null
mkdir -p /stack/state /stack/ai-cache /run/demo
cd /src && go build -buildvcs=false -o /stack/anas-api ./cmd/anas-api
worker=(python3 -m anas_ai.worker --fake)
if [ "$mode" = real ]; then
	# The Worker's runtime is pinned for x86_64; the demo pins the same versions for arm64.
	lock=/src/ai/requirements.lock
	[ "$(uname -m)" = aarch64 ] && lock=/src/ai/photo_video_demo/ai-runtime-arm64.lock
	python3 -m venv /stack/ai
	/stack/ai/bin/pip install --quiet --resume-retries 20 --timeout 60 --no-deps --require-hashes \
		--only-binary=:all: -r "$lock"
	worker=(/stack/ai/bin/python -m anas_ai.worker --cache-dir /stack/ai-cache
		--model /models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm
		--manifest /src/deploy/models/embeddinggemma-2-740m.json)
fi
# In the product systemd starts the Worker again on the next connection after it
# exits (idle, or told to release its models); here this loop does.
cd /src/ai && PYTHONDONTWRITEBYTECODE=1 setsid bash -c 'while :; do "$@"; sleep 1; done' worker \
	"${worker[@]}" --socket /run/demo/ai.sock >/stack/ai.log 2>&1 </dev/null &
ANAS_HOSTSTATE_MODE=fake ANAS_HTTP_ADDR=127.0.0.1:8080 ANAS_STATE_DIR=/stack/state ANAS_AI_SOCKET=/run/demo/ai.sock \
	setsid /stack/anas-api >/stack/api.log 2>&1 </dev/null &
# anas-api listens on loopback only (the LAN reaches it through Caddy); the
# published port reaches it through this relay, and only from the host's loopback.
setsid socat TCP-LISTEN:8081,fork,reuseaddr TCP:127.0.0.1:8080 </dev/null >/dev/null 2>&1 &
for _ in $(seq 100); do
	[ "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/api/v1/session)" != 000 ] && exit 0
	sleep 0.2
done
echo "anas-api did not start:" >&2
tail -20 /stack/api.log >&2
exit 1
EOF
	echo "API: http://127.0.0.1:$PORT, $mode AI (logs: docker exec $NAME cat /stack/api.log /stack/ai.log)"
}

# The first administrator can only be created over direct loopback, so seeding runs
# inside the container. Photos are generated stills named after what they would
# show; one is trashed and one is a video, for the tools' refusals.
seed() {
	: "${ANAS_DEMO_USER:?set ANAS_DEMO_USER}" "${ANAS_DEMO_PASSWORD:?set ANAS_DEMO_PASSWORD (12 characters or more)}"
	inside "$ANAS_DEMO_USER" "$ANAS_DEMO_PASSWORD" <<'EOF'
user=$1 password=$2 api=http://127.0.0.1:8080/api/v1 work=$(mktemp -d)
body=$(printf '{"username":"%s","password":"%s"}' "$user" "$password")
curl -sS -o /dev/null -X POST -H 'Content-Type: application/json' -d "$body" "$api/setup/admin"
csrf=$(curl -fsS -c "$work/jar" -X POST -H 'Content-Type: application/json' -d "$body" "$api/session" |
	python3 -c 'import json, sys; print(json.load(sys.stdin)["csrfToken"])')
library=$(curl -fsS -b "$work/jar" "$api/photos/libraries" |
	python3 -c 'import json, sys; print(next(l["id"] for l in json.load(sys.stdin)["items"] if l["kind"] == "private"))')
upload() {
	curl -fsS -b "$work/jar" -H "X-CSRF-Token: $csrf" -F "file=@$1" "$api/photos/libraries/$library/uploads" |
		python3 -c 'import json, sys; print(json.load(sys.stdin)["id"])'
}
for spec in "海边日落 1600x900 orange" "海边冲浪 1600x1200 deepskyblue" "沙滩椅 900x1600 khaki" \
	"生日蛋糕 1200x1200 pink" "山顶云海 1600x900 lightsteelblue" "城市夜景 1600x1000 midnightblue"; do
	read -r name size color <<<"$spec"
	ffmpeg -nostdin -loglevel error -f lavfi -i "testsrc2=s=$size:d=1,drawbox=c=$color@0.6:t=fill" \
		-frames:v 1 "$work/$name.jpg"
	upload "$work/$name.jpg" >/dev/null
done
ffmpeg -nostdin -loglevel error -f lavfi -i "testsrc2=s=640x360:d=2" -pix_fmt yuv420p "$work/海边视频.mp4"
upload "$work/海边视频.mp4" >/dev/null
ffmpeg -nostdin -loglevel error -f lavfi -i "color=c=gray:s=1200x800" -frames:v 1 "$work/海边旧照.jpg"
gone=$(upload "$work/海边旧照.jpg")
curl -fsS -o /dev/null -b "$work/jar" -H "X-CSRF-Token: $csrf" -X DELETE "$api/photos/assets/$gone"
rm -rf "$work"
echo "seeded: 6 photos, 1 video, 1 trashed photo ($gone) in library $library"
EOF
}

# Uploads from the host through the published port, so DIR needs no mount; the
# relay makes these sessions loopback ones, like the MCP server's.
import_dir() {
	: "${ANAS_DEMO_USER:?set ANAS_DEMO_USER}" "${ANAS_DEMO_PASSWORD:?set ANAS_DEMO_PASSWORD (12 characters or more)}"
	local dir=${1:?usage: $0 import DIR} api=http://127.0.0.1:$PORT/api/v1 jar body csrf library file quoted
	local uploaded=0 failed=0
	jar=$(mktemp)
	body=$(printf '{"username":"%s","password":"%s"}' "$ANAS_DEMO_USER" "$ANAS_DEMO_PASSWORD")
	inside "$body" <<'EOF'
curl -sS -o /dev/null -X POST -H 'Content-Type: application/json' -d "$1" http://127.0.0.1:8080/api/v1/setup/admin
EOF
	csrf=$(curl -fsS -c "$jar" -X POST -H 'Content-Type: application/json' -d "$body" "$api/session" |
		python3 -c 'import json, sys; print(json.load(sys.stdin)["csrfToken"])')
	library=$(curl -fsS -b "$jar" "$api/photos/libraries" |
		python3 -c 'import json, sys; print(next(l["id"] for l in json.load(sys.stdin)["items"] if l["kind"] == "private"))')
	while IFS= read -r -d '' file; do
		# curl reads a quoted name literally, commas and semicolons included.
		quoted=${file//\\/\\\\}
		quoted=${quoted//\"/\\\"}
		if curl -fsS -o /dev/null -b "$jar" -H "X-CSRF-Token: $csrf" -F "file=@\"$quoted\"" \
			"$api/photos/libraries/$library/uploads"; then
			uploaded=$((uploaded + 1))
		else
			failed=$((failed + 1))
			echo "failed: $file" >&2
		fi
	done < <(find "$dir" -type f \( -iname '*.jpg' -o -iname '*.jpeg' -o -iname '*.png' -o -iname '*.heic' \
		-o -iname '*.mp4' -o -iname '*.mov' \) -print0 | sort -z)
	rm -f "$jar"
	echo "imported $uploaded files into library $library ($failed failed)"
}

case ${1:-} in
start) start ;;
seed) seed ;;
import) import_dir "${2:-}" ;;
stop) docker rm -f "$NAME" >/dev/null && echo removed ;;
*)
	echo "usage: $0 start|seed|import DIR|stop" >&2
	exit 2
	;;
esac
