#!/usr/bin/env bash
# Smoke-Test für das Docker-Image (Concept §15: TC-OPS-01/02/03/04/09/10, TC-AUTH-01 im Container).
# Aufruf: scripts/smoke-test.sh <image>
set -euo pipefail
IMAGE=${1:?Image angeben}
NAME=smmyp-smoke-$$
VOL=smmyp-smoke-$$
PORT=${SMOKE_PORT:-18099}
TMP=$(mktemp -d)
fail() { echo "FEHLER: $*" >&2; docker logs "$NAME" 2>&1 | tail -20 >&2 || true; exit 1; }
cleanup() { docker rm -f "$NAME" "$NAME-none" "$NAME-perm" "$NAME-puid" >/dev/null 2>&1 || true; docker volume rm "$VOL" >/dev/null 2>&1 || true; rm -rf "$TMP"; }
trap cleanup EXIT
# Effektiver Benutzer des Hauptprozesses (UID oder Name, je nach Docker-Variante).
top_user() { docker top "$1" -o pid,user | awk 'NR==2{print $2}'; }
is_user() { [ "$1" = "$2" ] || [ "$1" = "$(getent passwd "$2" 2>/dev/null | cut -d: -f1)" ] || { [ "$2" = "$(id -u)" ] && [ "$1" = "$(id -un)" ]; }; }

echo "== TC-OPS-01 Image-Größe"
SIZE=$(docker image inspect "$IMAGE" --format '{{.Size}}')
echo "   $((SIZE / 1024 / 1024)) MB"
[ "$SIZE" -lt $((30 * 1024 * 1024)) ] || fail "Image größer als 30 MB"

echo "== TC-OPS-03 Start read-only als nonroot"
docker run -d --name "$NAME" --read-only --security-opt no-new-privileges:true -p "127.0.0.1:$PORT:8080" -v "$VOL:/data" "$IMAGE" >/dev/null
for i in $(seq 1 30); do
  [ "$(docker inspect -f '{{.State.Health.Status}}' "$NAME")" = healthy ] && break
  sleep 1
done
[ "$(docker inspect -f '{{.State.Health.Status}}' "$NAME")" = healthy ] || fail "Container wird nicht healthy"
[ "$(docker exec "$NAME" /smmyp version)" != "" ] || fail "version"
U=$(top_user "$NAME")
is_user "$U" 65532 || fail "läuft nicht als 65532 (user=$U)"
curl -fsS "http://127.0.0.1:$PORT/healthz" | grep -q ok || fail "healthz"

echo "== TC-AUTH-01 Erst-Setup im Container"
TOKEN=$(docker logs "$NAME" 2>&1 | grep -o '"setup_token":"[A-Z0-9]*"' | cut -d'"' -f4)
[ -n "$TOKEN" ] || fail "kein Setup-Token im Log"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -c "$TMP/cj" -d "token=$TOKEN&username=smoke&password=smoke-passwort-1" "http://127.0.0.1:$PORT/setup")
[ "$CODE" = 303 ] || fail "Setup: HTTP $CODE"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -b "$TMP/cj" --data-urlencode "title=Smoke" -d "provider=1&primary=1" --data-urlencode "field_prompt=Rauchtest" "http://127.0.0.1:$PORT/p")
[ "$CODE" = 303 ] || fail "Prompt anlegen: HTTP $CODE"

echo "== TC-OPS-02 Speicher"
for i in $(seq 1 200); do curl -s -o /dev/null -b "$TMP/cj" "http://127.0.0.1:$PORT/?q=rauch"; done
MEM=$(docker stats --no-stream --format '{{.MemUsage}}' "$NAME" | cut -d/ -f1 | tr -d ' ')
echo "   $MEM nach 200 Suchen"
case "$MEM" in *GiB) fail "Speicher $MEM" ;; esac
MB=$(echo "$MEM" | sed -E 's/MiB//; s/KiB/ /' | awk '{print int($1)}')
[ "$MB" -lt 80 ] || fail "Speicher $MEM über 80 MB"

echo "== TC-OPS-09 Herunterfahren und Neustart mit Daten"
START=$(date +%s)
docker stop -t 10 "$NAME" >/dev/null
DUR=$(( $(date +%s) - START ))
echo "   gestoppt in ${DUR}s"
[ "$DUR" -lt 6 ] || fail "Herunterfahren dauert ${DUR}s"
docker start "$NAME" >/dev/null
for i in $(seq 1 30); do curl -fs "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break; sleep 1; done
CODE=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/setup")
[ "$CODE" = 404 ] || fail "Setup nach Neustart wieder offen ($CODE)"
curl -s -c "$TMP/cj2" -o /dev/null -d "username=smoke&password=smoke-passwort-1" "http://127.0.0.1:$PORT/login"
curl -s -b "$TMP/cj2" "http://127.0.0.1:$PORT/?q=rauchtest" | grep -q "Smoke" || fail "Prompt nach Neustart nicht gefunden"
docker exec "$NAME" /smmyp backup | grep -q "/data/backups/smmyp-" || fail "Backup-Befehl"

echo "== TC-OPS-10 ohne Netzwerk"
docker run -d --name "$NAME-none" --network none --read-only "$IMAGE" >/dev/null
sleep 3
docker exec "$NAME-none" /smmyp healthcheck || fail "Healthcheck ohne Netzwerk"

echo "== TC-OPS-04 Datenordner ohne Schreibrechte"
mkdir -p "$TMP/noperm" && chmod 755 "$TMP/noperm"
docker run -d --name "$NAME-perm" -v "$TMP/noperm:/data" "$IMAGE" >/dev/null
for i in $(seq 1 15); do [ "$(docker inspect -f '{{.State.Running}}' "$NAME-perm")" = false ] && break; sleep 1; done
if [ "$(docker inspect -f '{{.State.Running}}' "$NAME-perm")" = true ]; then
  [ "$(uname -s)" = Linux ] && fail "Start trotz fehlender Schreibrechte"
  echo "   übersprungen: Bind-Mounts auf $(uname -s) setzen keine Linux-Dateirechte durch"
else
  [ "$(docker inspect -f '{{.State.ExitCode}}' "$NAME-perm")" != 0 ] || fail "Exit-Code 0 trotz fehlender Rechte"
  docker logs "$NAME-perm" 2>&1 | grep -q "nicht beschreibbar" || fail "keine verständliche Fehlermeldung"
fi

echo "== Unraid-Weg: --user 0:0 mit PUID/PGID"
mkdir -p "$TMP/puid"
docker run -d --name "$NAME-puid" --user 0:0 -e PUID="$(id -u)" -e PGID="$(id -g)" -v "$TMP/puid:/data" "$IMAGE" >/dev/null
for i in $(seq 1 20); do docker logs "$NAME-puid" 2>&1 | grep -q "SMMYP gestartet" && break; sleep 1; done
docker logs "$NAME-puid" 2>&1 | grep -q "root abgegeben" || { docker logs "$NAME-puid" >&2; fail "PUID/PGID: root nicht abgegeben"; }
U=$(top_user "$NAME-puid")
is_user "$U" "$(id -u)" || fail "PUID: läuft als $U"
[ -f "$TMP/puid/smmyp.db" ] || fail "PUID: Datenbank fehlt"

echo "Smoke-Test bestanden."
