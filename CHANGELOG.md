# Changelog

## 0.1.0-rc1 – 31.10.2026

Erster Release-Kandidat (Phase 0 + 1 aus dem Konzept).

- Prompts anlegen, bearbeiten, löschen (Papierkorb, 30 Tage wiederherstellbar)
- Ein Prompt gehört zu **einem oder mehreren** Anbietern; mehrere Reiter je Karte, Feld-Union (Suno: Style/Lyrics/Exclude, Midjourney: Prompt/Parameter, Claude: System/Prompt …), Nutzungszähler je Anbieter
- 22 Anbieter als Startliste in 6 Kategorien, eigene Anbieter anlegbar
- Volltextsuche (SQLite FTS5) mit Syntax `@anbieter`, `#tag`, `"Phrase"`, `-wort`, `fav:`, `used:7d`, `kat:musik`; Live-Suche, Facetten je KI, Treffer-Markierung
- Nach KI gruppierte Übersicht, Schubladen-Navigation, **Karten- und Listenansicht** (Taste `v`)
- Kopieren mit Fallback für HTTP im Heimnetz, „Kopiert"-Stempel, Tastaturkürzel
- Multiuser: Erst-Setup mit Token, Einladungslinks, Rollen, Konten sperren, Rate-Limit beim Login
- Sicherheit: strikte CSP, Cross-Origin-Schutz, Host-Prüfung gegen DNS-Rebinding, argon2id, Sessions nur als Hash gespeichert
- Betrieb: nächtliche Backups mit Rotation, Backup vor Migrationen, JSON-Export, `healthcheck`/`backup`/`reindex`/`passwd`-Befehle
- Docker-Image (distroless, nonroot, read-only-fähig), Multi-Arch amd64/arm64, Unraid-Template mit PUID/PGID
- Design „Karteikasten": Papier/Dunkelkammer, IBM Plex (selbst gehostet), responsive mit Suche unten auf dem Handy

Bekannte Einschränkungen: siehe README, Abschnitt „Stand 0.1-rc1".
