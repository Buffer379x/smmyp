# SMMYP – Show Me My Prompts

<img src="unraid/smmyp-icon.png" alt="SMMYP" width="96" align="right">

Ein kleines, schnelles Prompt-Archiv für dein Heimnetz. SMMYP sammelt deine KI-Prompts (Suno-Styles, Claude-Aufträge, Midjourney-Bilder …), macht sie durchsuchbar und listet sie **nach KI gruppiert** wieder auf. Ein Klick kopiert den Prompt in die Zwischenablage.

- **Karteikasten statt Chatblase:** Jede KI ist eine Schublade mit farbigem Reiter, jeder Prompt eine Karteikarte. Karten- oder Listenansicht, hell oder dunkel.
- **Ein Prompt, mehrere KIs:** Ein Prompt kann zu einem oder mehreren Anbietern gehören. Die Felder richten sich nach dem Anbieter: Suno Style/Lyrics/Exclude, Midjourney Prompt/Parameter, Claude System/Prompt. Jedes Feld hat einen eigenen Kopier-Button.
- **Suche über alles oder gezielt:** `@suno dark #lofi "night drive" -country`, mit Live-Ergebnissen, Facetten je KI und markierten Treffern.
- **Platzhalter:** `{{genre}}` oder `{{bpm=98}}` im Prompt; beim Kopieren fragt SMMYP die Werte ab (zuletzt benutzte sind vorbelegt).
- **Versionen, Sammlungen, Bewertung:** Jede Änderung wird als Version gesichert (mit Wort-Diff). Sammlungen bündeln Prompts quer über KIs, dazu Sterne und Ergebnis-Links.
- **KI-Hilfe (optional, standardmäßig aus):** Tag- und Anbieter-Vorschläge über Wörterbücher, ähnliche eigene Prompts und einen Klassifikator, der aus deinen Tags lernt. Klassisches Machine Learning direkt im Programm, ohne Internet und ohne Sprachmodell.
- **Multiuser:** Private Sammlungen je Konto, neue Konten per Einladungslink oder per SSO über den Reverse Proxy.
- **Klein:** ein Go-Binary mit SQLite und ohne externe Dienste. Docker-Image ~20 MB, im Leerlauf < 40 MB RAM. Keine Verbindungen ins Internet.

> **Stand:** 0.2.0 – siehe [CHANGELOG](CHANGELOG.md).

## Schnellstart (Docker Compose)

```yaml
services:
  smmyp:
    image: ghcr.io/buffer379x/smmyp:latest
    restart: unless-stopped
    ports: ["8080:8080"]
    volumes: ["smmyp-data:/data"]
    environment:
      TZ: "Europe/Berlin"
    read_only: true
volumes:
  smmyp-data:
```

```sh
docker compose up -d
docker compose logs smmyp | grep setup_token     # Setup-Token für die Ersteinrichtung
```

Dann `http://<server-ip>:8080` öffnen, das Token eingeben und das erste Konto anlegen. Es wird Admin. Weitere Personen lädst du unter **Menü → Verwaltung → Einladungslink** ein.

## Unraid

Das Template liegt unter [`unraid/smmyp.xml`](unraid/smmyp.xml). Zwei Wege:

- **Template-Repository:** *Docker → Template Repositories* um `https://github.com/Buffer379x/smmyp` ergänzen, speichern, dann *Add Container → Template: SMMYP*.
- **Manuell:** Auf dem Unraid-Server
  `wget -O /boot/config/plugins/dockerMan/templates-user/my-SMMYP.xml https://raw.githubusercontent.com/Buffer379x/smmyp/main/unraid/smmyp.xml`
  ausführen, danach *Docker → Add Container → Template: SMMYP*.

Danach:

1. Pfad und Port prüfen, *Apply*.
2. Nach dem Start unter **Logs** das `setup_token` ablesen und die WebUI öffnen.

Das Template startet den Container mit `--user 0:0` und `PUID=99`/`PGID=100`. SMMYP setzt die Rechte des appdata-Ordners auf `nobody:users` und gibt root sofort danach ab. Die Datenbank gehört am besten auf den Cache/Pool (`/mnt/cache/appdata/smmyp`), nicht über `/mnt/user` aufs Array (FUSE). SMMYP warnt im Log, wenn es so ein Dateisystem erkennt.

## Konfiguration

| Variable | Standard | Zweck |
|---|---|---|
| `SMMYP_LISTEN` | `:8080` | Adresse und Port |
| `SMMYP_DATA_DIR` | `/data` | Datenbank und Backups |
| `SMMYP_BASE_URL` | – | Adresse, unter der SMMYP aufgerufen wird (bei Reverse Proxy mit HTTPS) |
| `SMMYP_ALLOWED_HOSTS` | – | Zusätzliche Hostnamen (Komma-getrennt, `*.fritz.box` erlaubt). IP-Adressen, `localhost` und der Host aus `SMMYP_BASE_URL` sind immer erlaubt. Schutz vor DNS-Rebinding |
| `SMMYP_SECURE_COOKIES` | `auto` | `true` bei `https://`-Basis-URL |
| `SMMYP_TRUSTED_PROXIES` | – | CIDR des Reverse Proxys (für echte Client-IPs) |
| `SMMYP_REGISTRATION` | `invite` | `invite` · `open` · `closed` |
| `SMMYP_BACKUP_TIME` | `03:30` | Tägliches Backup, `off` = aus |
| `SMMYP_BACKUP_KEEP` | `14` | Anzahl aufbewahrter Backups |
| `SMMYP_ADMIN_USER` / `SMMYP_ADMIN_PASSWORD_FILE` | – | Ersten Admin automatisch anlegen (statt Setup-Token) |
| `SMMYP_ML` | `on` | `off` schaltet die optionale KI-Hilfe für alle ab (Standard: jeder Benutzer kann sie in den Einstellungen einschalten) |
| `SMMYP_AUTH_PROXY_HEADER` | – | z. B. `Remote-User`: Anmeldung über den SSO-Header eines Reverse Proxys (Authelia, Authentik). Nur von `SMMYP_TRUSTED_PROXIES` |
| `SMMYP_AUTH_PROXY_AUTOCREATE` | `false` | unbekannte Proxy-Benutzer automatisch anlegen (der erste wird Admin) |
| `SMMYP_LOG_LEVEL` | `info` | `debug` · `info` · `warn` · `error` |
| `PUID` / `PGID` | `65532` | Nur wenn als root gestartet: Besitzer des Datenordners, danach wird root abgegeben |

## HTTPS im Heimnetz

SMMYP funktioniert auch über reines HTTP. Browser erlauben einige Funktionen aber nur über HTTPS: die moderne Zwischenablage (SMMYP nutzt dann einen Fallback) und künftig die App-Installation (PWA). Wege dorthin, ohne etwas ins Internet freizugeben:

1. **Eigene Domain + Let's Encrypt per DNS-Challenge** (Caddy, Traefik, Nginx Proxy Manager) und ein lokaler DNS-Eintrag im Router oder in Pi-hole. Empfohlen.
2. **Tailscale** mit MagicDNS und `tailscale cert`
3. **Eigene Zertifizierungsstelle** (Caddy `tls internal`, mkcert). Das Root-Zertifikat muss dann auf jedem Gerät installiert werden.

Hinter dem Proxy `SMMYP_BASE_URL=https://…` und `SMMYP_TRUSTED_PROXIES` setzen.

## Bedienung

| Taste | Aktion |
|---|---|
| `/` | Suche |
| `Esc` | Suche leeren |
| `j` / `k` | nächste / vorige Karte |
| `Enter` | öffnen |
| `c` | kopieren |
| `f` | Favorit |
| `n` | neuer Prompt |
| `v` | Karten ↔ Liste |
| `x` | Prompt auswählen (Liste, für Mehrfachaktionen) |
| `Strg/⌘ K` | Befehlspalette |

| Suche | Bedeutung |
|---|---|
| `night drive` | beide Wörter, als Präfix (`nig` findet „night") |
| `"night drive"` | exakte Phrase |
| `@suno` | nur Suno (mehrere `@…` = einer davon) |
| `#lofi` | mit Tag (mehrere = alle) |
| `-country` | ohne dieses Wort |
| `~solo` | Teilwort (findet „Gitarrensolo“); bei sehr wenigen Treffern sucht SMMYP automatisch so |
| `bew:4` | mindestens 4 Sterne |
| `kat:musik` | alle Musik-KIs |
| `fav:` · `used:7d` | Favoriten · in den letzten 7 Tagen benutzt |

## Backup & Wiederherstellung

- Automatisch täglich nach `/data/backups` (konsistenter Schnappschuss per `VACUUM INTO`), die letzten 14 werden aufbewahrt. Vor jeder Schema-Migration entsteht zusätzlich ein Backup.
- Manuell: **Verwaltung → Backup jetzt** oder `docker exec smmyp /smmyp backup`
- **Wiederherstellen:** Container stoppen, `smmyp.db` im Datenordner durch das Backup ersetzen (die Dateien `smmyp.db-wal` und `smmyp.db-shm` löschen), Container starten.
- Export der eigenen Prompts als JSON: **Menü → Export**

## Befehle

```sh
docker exec smmyp /smmyp backup            # Backup jetzt
docker exec smmyp /smmyp reindex           # Suchindex neu aufbauen
echo 'neues-passwort-123' | docker exec -i smmyp /smmyp passwd <name>   # Passwort zurücksetzen
docker exec smmyp /smmyp version
```

## KI-Hilfe (optional)

Standardmäßig **aus**. Einschalten unter *Einstellungen → KI-Hilfe*. Dann:

1. **Regeln & Wörterbücher:** Genres, Stimmungen, Tempo-Klassen aus „98 bpm", Bildformat aus `--ar` sowie eigene Regeln (*Tags & KI-Hilfe*).
2. **Ähnliche Prompts:** ab 20 getaggten Prompts schlägt SMMYP Tags vor, die ähnliche eigene Prompts tragen.
3. **Gelernter Klassifikator** (Complement Naive Bayes): ab 50 Prompts mit mindestens 5 Beispielen je Tag. Training alle 5 Minuten bei Änderungen, ein Modell je Benutzer. Ist die gemessene Trefferquote zu niedrig, bleibt dieser Teil stumm.

Jeder Vorschlag nennt seinen Grund. Übernehmen und Ablehnen fließen ins Lernen ein. Inhalte aus *Exclude*/*Negativ-Prompt* werden ignoriert. Beim Ausschalten werden die Modelle gelöscht. Unabhängig von der Option laufen die Warnungen vor Duplikaten und vor versehentlich eingefügten API-Schlüsseln.

## Weitere Funktionen

- **Import:** *Menü → Import* nimmt SMMYP-Exporte (JSON) und Tabellen (CSV, Komma oder Semikolon) an. Inhaltsgleiche Prompts werden übersprungen.
- **Bookmarklet:** Unter *Einstellungen* gibt es einen Lesezeichen-Link, der markierten Text samt Quelle ins Formular übernimmt.
- **App-Installation (PWA):** Mit HTTPS installierbar, auf Android mit „Teilen → SMMYP", dazu ein Offline-Lesecache für besuchte Seiten.
- **Tag-Verwaltung:** Umbenennen und Zusammenführen (der alte Name wird automatisch Alias), eigene Aliase.

## Entwicklung

```sh
go test ./...                          # Unit-, Integrations- und HTTP-Tests
go test -run XXX -bench Search10k ./internal/prompts   # Suche bei 10.000 Prompts
SMMYP_DATA_DIR=./tmp SMMYP_LISTEN=127.0.0.1:8080 go run ./cmd/smmyp
docker build -t smmyp:dev --build-arg VERSION=dev .
```

Aufbau: `cmd/smmyp` (Binary), `internal/` (`store`, `auth`, `providers`, `prompts`, `search`, `tags`, `backup`, `web`). Templates, CSS, JS und Schriften sind eingebettet.

## Lizenz

[MIT](LICENSE). Die Schrift IBM Plex steht unter der SIL Open Font License (`internal/web/static/fonts/OFL.txt`).
