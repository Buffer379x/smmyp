# Changelog

## 0.2.0 – 31.10.2026 (Phase 2)

- **Platzhalter** `{{name}}` / `{{name=Standard}}` mit Ausfüll-Dialog beim Kopieren, zuletzt benutzte Werte, `\{{` für wörtliche Klammern, pro Prompt abschaltbar
- **Versionen** mit Wort-Diff und Wiederherstellen (bis 50 je Prompt)
- **Sammlungen** quer über Anbieter, **Bewertung** (1–5, `bew:4`, Sortierung), **Ergebnis-Links**, Quelle
- **Teilwortsuche** (FTS5-Trigram): `~wort`, automatisch bei weniger als 5 Treffern
- **Tags:** Aliase (z. B. `lofi` → `lo-fi`), Umbenennen/Zusammenführen, eigene Regeln
- **KI-Hilfe (optional, standardmäßig aus):** Wörterbücher, ähnliche Prompts, Complement-Naive-Bayes-Klassifikator für Tags und Anbieter, Begründungen, Lernen aus Übernehmen/Ablehnen; `SMMYP_ML=off` schaltet sie instanzweit ab
- **Hinweise beim Speichern:** fast gleiche Prompts (SimHash), versehentlich eingefügte API-Schlüssel
- **Ähnliche Prompts** in der Detailansicht, Spracherkennung (de/en)
- **Import** (JSON, CSV), **Mehrfachauswahl** in der Liste (Tag, Anbieter, Sammlung, Papierkorb), **Befehlspalette** `⌘K`
- **PWA** (Manifest, Icons, Service Worker, „Teilen →"), **Bookmarklet**, Anbieter-Erkennung aus der Quell-URL
- **Proxy-Login** (SSO-Header, nur von vertrauenswürdigen Proxys)
- Schema v2 mit automatischem Backup vor der Migration

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
