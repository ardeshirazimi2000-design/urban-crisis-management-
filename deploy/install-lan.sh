#!/usr/bin/env bash
# One-step installer for an office (LAN) TEST server — Ubuntu Server 24.04.
#
#   curl -fsSL https://raw.githubusercontent.com/ardeshirazimi2000-design/urban-crisis-management-/claude/urban-crisis-platform-mvp/deploy/install-lan.sh -o install-lan.sh
#   sudo bash install-lan.sh
#
# Safe to re-run: it updates the code and keeps existing secrets and data.
# This is a TEST deployment: login uses local development tokens (anyone on the LAN can sign in with any role).
# Never put real personal data on it and never expose it to the Internet (no port forwarding on the router).
set -Eeuo pipefail

REPO_URL=${REPO_URL:-https://github.com/ardeshirazimi2000-design/urban-crisis-management-.git}
BRANCH=${BRANCH:-claude/urban-crisis-platform-mvp}
INSTALL_DIR=${INSTALL_DIR:-/opt/urban-crisis}
BACKUP_DIR=${BACKUP_DIR:-/var/backups/urban-crisis}
WEB_PORT=${WEB_PORT:-80}
# Candidate mirrors, tried in order; override with space-separated lists.
REGISTRY_MIRRORS=${REGISTRY_MIRRORS:-"https://docker.arvancloud.ir https://docker.iranserver.com https://registry.docker.ir"}
NPM_CANDIDATES=${NPM_CANDIDATES:-"https://registry.npmjs.org/ https://registry.npmmirror.com/"}
PIP_CANDIDATES=${PIP_CANDIDATES:-"https://pypi.org/simple https://mirror-pypi.runflare.com/simple"}
# Testing hooks (not needed on a real server).
SKIP_PACKAGES=${SKIP_PACKAGES:-0}
SKIP_FIREWALL=${SKIP_FIREWALL:-0}
SKIP_DOCKER_CONFIG=${SKIP_DOCKER_CONFIG:-0}
COMPOSE_EXTRA_FILES=${COMPOSE_EXTRA_FILES:-}

log()  { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
ok()   { printf '   \033[32m✔\033[0m %s\n' "$*"; }
warn() { printf '   \033[33m!\033[0m %s\n' "$*"; }
die()  { printf '\n\033[1;31m✖ %s\033[0m\n' "$*" >&2; exit 1; }
trap 'die "Failed at line $LINENO. Fix the problem above and re-run: sudo bash $0"' ERR

[[ $EUID -eq 0 ]] || die "Run with sudo: sudo bash $0"
. /etc/os-release
[[ "${ID:-}" == "ubuntu" ]] || die "This installer supports Ubuntu Server 24.04 only."
[[ "${VERSION_ID:-}" == "24.04" ]] || warn "Tested on Ubuntu 24.04; detected ${VERSION_ID:-unknown}."

# ---------------------------------------------------------------------------
log "Detecting network"
detect_ip() {
  { ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}'; } || true
  { hostname -I 2>/dev/null | awk '{print $1}'; } || true
}
SERVER_IP=${SERVER_IP:-$(detect_ip | grep -m1 -E '^[0-9.]+$' || true)}
[[ -n "$SERVER_IP" ]] || die "Could not detect the server IP. Re-run with SERVER_IP=192.168.x.y"
LAN_CIDR=${LAN_CIDR:-$({ ip -o -f inet addr show 2>/dev/null | awk -v ip="$SERVER_IP" '$4 ~ "^"ip"/" {print $4; exit}'; } || true)}
LAN_CIDR=${LAN_CIDR:-$SERVER_IP/24}
# Normalise 192.168.1.16/24 -> 192.168.1.0/24
LAN_CIDR=$(python3 -c "import ipaddress,sys;print(ipaddress.ip_interface(sys.argv[1]).network)" "$LAN_CIDR")
ok "Server IP: $SERVER_IP   LAN: $LAN_CIDR"
MEM_GB=$(awk '/MemTotal/ {printf "%d", $2/1024/1024}' /proc/meminfo)
DISK_GB=$(df -BG --output=avail / | tail -1 | tr -dc '0-9')
(( MEM_GB >= 7 )) && ok "RAM: ${MEM_GB} GB" || warn "RAM ${MEM_GB} GB — at least 8 GB is recommended."
(( DISK_GB >= ${MIN_DISK_GB:-30} )) && ok "Free disk: ${DISK_GB} GB" || die "Only ${DISK_GB} GB free on /; at least ${MIN_DISK_GB:-30} GB is needed."

# ---------------------------------------------------------------------------
if [[ "$SKIP_PACKAGES" != 1 ]]; then
  log "Installing Docker and tools from the Ubuntu archive"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq docker.io docker-compose-v2 docker-buildx git curl openssl ufw ca-certificates cron python3 >/dev/null
  systemctl enable --now docker >/dev/null
  ok "$(docker --version)"
  ok "$(docker compose version)"
fi
command -v docker >/dev/null || die "Docker is not installed."
docker compose version >/dev/null 2>&1 || die "docker compose v2 is not available."

# ---------------------------------------------------------------------------
log "Checking access to package sources"
reachable() { curl -sS -o /dev/null --max-time 12 -w '%{http_code}' "$1" 2>/dev/null | grep -qE '^(2|3|401)'; }
# Probes download a real package file: some sources answer index requests but refuse downloads (403).
downloads() { curl -fsSL --max-time 30 -o /dev/null "$1" 2>/dev/null; }
npm_ok() { downloads "${1%/}/ms/-/ms-2.1.3.tgz"; }
pip_ok() {
  local page file
  page=$(curl -fsSL --max-time 20 "${1%/}/six/" 2>/dev/null) || return 1
  file=$(python3 -c 'import re,sys,urllib.parse as u
m=re.search(r"href=\"([^\"]+\.whl)[^\"]*\"", sys.stdin.read())
print(u.urljoin(sys.argv[1], m.group(1)) if m else "")' "${1%/}/six/" <<<"$page")
  [[ -n "$file" ]] && downloads "$file"
}
first_ok() { local check=$1; shift; for c in "$@"; do "$check" "$c" && { echo "$c"; return 0; }; done; return 1; }

DOCKERHUB_DIRECT=0
reachable "https://registry-1.docker.io/v2/" && curl -sS -o /dev/null --max-time 12 -w '%{http_code}' \
  "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/alpine:pull" | grep -q '^200' && DOCKERHUB_DIRECT=1
WORKING_MIRRORS=()
for m in $REGISTRY_MIRRORS; do reachable "${m%/}/v2/" && WORKING_MIRRORS+=("$m"); done
if (( ${#WORKING_MIRRORS[@]} )); then ok "Docker registry mirrors: ${WORKING_MIRRORS[*]}"; fi
(( DOCKERHUB_DIRECT )) && ok "Docker Hub reachable directly"
(( DOCKERHUB_DIRECT || ${#WORKING_MIRRORS[@]} )) || die "Neither Docker Hub nor any registry mirror is reachable. Set REGISTRY_MIRRORS=\"https://your-mirror\" or configure a proxy, then re-run."

# shellcheck disable=SC2086
NPM_URL=$(first_ok npm_ok $NPM_CANDIDATES) || die "No npm registry allows downloads (tried: $NPM_CANDIDATES). Set NPM_CANDIDATES=\"https://your-mirror/\"."
# shellcheck disable=SC2086
PIP_URL=$(first_ok pip_ok $PIP_CANDIDATES) || die "No Python package index allows downloads (tried: $PIP_CANDIDATES). Set PIP_CANDIDATES=\"https://your-mirror/simple\"."
timeout 60 git ls-remote --exit-code --heads "$REPO_URL" "$BRANCH" >/dev/null 2>&1 \
  || die "Cannot reach the code repository ($REPO_URL, branch $BRANCH). Check access to GitHub."
ok "npm: $NPM_URL   pip: $PIP_URL   (Go packages are bundled in the repository)"

# ---------------------------------------------------------------------------
if [[ "$SKIP_DOCKER_CONFIG" != 1 ]]; then
  log "Configuring the Docker daemon (registry mirrors, log rotation)"
  mkdir -p /etc/docker
  [[ -f /etc/docker/daemon.json ]] && cp /etc/docker/daemon.json "/etc/docker/daemon.json.bak.$(date +%s)"
  python3 - "${WORKING_MIRRORS[@]}" <<'PY'
import json, os, sys
path = "/etc/docker/daemon.json"
cfg = {}
if os.path.exists(path):
    try:
        cfg = json.load(open(path))
    except ValueError:
        cfg = {}
cfg["registry-mirrors"] = sys.argv[1:]
cfg.setdefault("log-driver", "json-file")
cfg.setdefault("log-opts", {"max-size": "20m", "max-file": "5"})
cfg.setdefault("live-restore", True)
json.dump(cfg, open(path, "w"), indent=2)
PY
  systemctl restart docker
  ok "daemon.json updated"
fi

# ---------------------------------------------------------------------------
log "Fetching the code ($BRANCH)"
if [[ -d "$INSTALL_DIR/.git" ]]; then
  git -C "$INSTALL_DIR" fetch --depth 1 origin "$BRANCH"
  git -C "$INSTALL_DIR" checkout -q -B "$BRANCH" FETCH_HEAD
else
  git clone -q --depth 1 --branch "$BRANCH" "$REPO_URL" "$INSTALL_DIR"
fi
ok "$(git -C "$INSTALL_DIR" log -1 --format='%h %s')"

# ---------------------------------------------------------------------------
log "Writing configuration (existing secrets are kept)"
ENV_FILE="$INSTALL_DIR/.env"
getenv() { [[ -f "$ENV_FILE" ]] && grep -E "^$1=" "$ENV_FILE" | head -1 | cut -d= -f2- || true; }
secret() { local v; v=$(getenv "$1"); [[ -n "$v" && "$v" != *change-me* && "$v" != local-only ]] && echo "$v" || openssl rand -hex "$2"; }
POSTGRES_PASSWORD=$(secret POSTGRES_PASSWORD 24)
DEV_JWT_SECRET=$(secret DEV_JWT_SECRET 32)
MEDIA_URL_SECRET=$(secret MEDIA_URL_SECRET 32)
PUBLIC_BASE_URL="http://$SERVER_IP"; [[ "$WEB_PORT" != 80 ]] && PUBLIC_BASE_URL="$PUBLIC_BASE_URL:$WEB_PORT"
umask 077
cat > "$ENV_FILE" <<EOF
# Generated by deploy/install-lan.sh on $(date -Iseconds). Keep private.
APP_ENV=local
POSTGRES_PASSWORD=$POSTGRES_PASSWORD
DEV_JWT_SECRET=$DEV_JWT_SECRET
MEDIA_URL_SECRET=$MEDIA_URL_SECRET
WEB_BIND=0.0.0.0:$WEB_PORT
PUBLIC_BASE_URL=$PUBLIC_BASE_URL
NPM_REGISTRY=$NPM_URL
PIP_INDEX_URL=$PIP_URL
EOF
umask 022
ok "$ENV_FILE"

# ---------------------------------------------------------------------------
log "Building images (first run takes 5–15 minutes)"
cd "$INSTALL_DIR"
COMPOSE=(docker compose -f docker-compose.yml)
for f in $COMPOSE_EXTRA_FILES; do COMPOSE+=(-f "$f"); done
for attempt in 1 2 3; do
  if "${COMPOSE[@]}" build; then break; fi
  (( attempt == 3 )) && die "Image build failed 3 times (network or mirror problem). See the error above."
  warn "Build failed (attempt $attempt); retrying in 30s…"; sleep 30
done
ok "Images built"

log "Starting services"
"${COMPOSE[@]}" up -d --remove-orphans
for _ in $(seq 1 60); do
  curl -fsS --max-time 3 "http://127.0.0.1:8080/health/ready" >/dev/null 2>&1 && break
  sleep 5
done
curl -fsS --max-time 3 "http://127.0.0.1:8080/health/ready" >/dev/null || { "${COMPOSE[@]}" ps; die "API did not become ready. Inspect: docker compose -f $INSTALL_DIR/docker-compose.yml logs api migrate"; }
curl -fsS --max-time 5 -o /dev/null "http://127.0.0.1:$WEB_PORT/" || die "Console is not answering on port $WEB_PORT."
ok "All services are up"

# ---------------------------------------------------------------------------
if [[ "$SKIP_FIREWALL" != 1 ]]; then
  log "Firewall"
  ufw --force default deny incoming >/dev/null
  ufw --force default allow outgoing >/dev/null
  ufw allow from "$LAN_CIDR" to any port 22 proto tcp >/dev/null
  ufw allow from "$LAN_CIDR" to any port "$WEB_PORT" proto tcp >/dev/null
  ufw --force enable >/dev/null
  ok "SSH and the console are allowed from $LAN_CIDR only"
  warn "Ports published by Docker bypass ufw; keep this server off the Internet (no port forwarding)."
fi

# ---------------------------------------------------------------------------
log "Daily backup (02:30, kept 14 days)"
mkdir -p "$BACKUP_DIR"; chmod 700 "$BACKUP_DIR"
cat > /usr/local/sbin/urban-crisis-backup <<EOF
#!/usr/bin/env bash
# Database dump + media volume archive. Restore drill: tests/disaster-drills/backup-restore.sh
set -euo pipefail
cd "$INSTALL_DIR"
ts=\$(date +%F-%H%M)
docker compose exec -T postgres pg_dump -U crisis -Fc crisis > "$BACKUP_DIR/db-\$ts.dump"
docker run --rm -v urban-crisis_objects:/data:ro -v "$BACKUP_DIR":/backup postgis/postgis:16-3.4 \
  tar czf "/backup/media-\$ts.tgz" -C /data .
find "$BACKUP_DIR" -type f -mtime +14 -delete
echo "backup ok \$ts"
EOF
chmod 750 /usr/local/sbin/urban-crisis-backup
echo "30 2 * * * root /usr/local/sbin/urban-crisis-backup >> /var/log/urban-crisis-backup.log 2>&1" > /etc/cron.d/urban-crisis-backup
/usr/local/sbin/urban-crisis-backup >/dev/null && ok "First backup written to $BACKUP_DIR"

# ---------------------------------------------------------------------------
cat <<EOF

$(printf '\033[1;32m')Installation complete.$(printf '\033[0m')

  Console (from any office computer):  $PUBLIC_BASE_URL
  API for the mobile apps:             $PUBLIC_BASE_URL/api/v1

  Sign in with a sample role on the login page (operator, commander, resource manager, GIS, security).
  Sample data (organisations, facilities, resources) is fictional.

  Useful commands:
    cd $INSTALL_DIR
    sudo docker compose ps                 # status
    sudo docker compose logs -f api        # logs
    sudo bash deploy/install-lan.sh        # update to the latest code (keeps data)
    sudo urban-crisis-backup               # manual backup

  TEST deployment only: development sign-in, no real personal data, no Internet exposure.
EOF
