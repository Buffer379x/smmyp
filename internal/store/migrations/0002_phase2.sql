-- SMMYP Schema v2 (0.2): Versionen, Links, Aliase, Regeln, ML, Sammlungen, Variablen, Teilwortsuche.

ALTER TABLE prompts ADD COLUMN rating     INTEGER CHECK (rating BETWEEN 1 AND 5);
ALTER TABLE prompts ADD COLUMN simhash    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE prompts ADD COLUMN language   TEXT NOT NULL DEFAULT '';
ALTER TABLE prompts ADD COLUMN source_url TEXT NOT NULL DEFAULT '';
ALTER TABLE prompts ADD COLUMN vars_off   INTEGER NOT NULL DEFAULT 0;

CREATE TABLE prompt_versions (
  id         INTEGER PRIMARY KEY,
  prompt_id  INTEGER NOT NULL REFERENCES prompts(id) ON DELETE CASCADE,
  title      TEXT NOT NULL,
  fields     TEXT NOT NULL,
  notes      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX prompt_versions_prompt ON prompt_versions(prompt_id, id DESC);

CREATE TABLE prompt_links (
  id        INTEGER PRIMARY KEY,
  prompt_id INTEGER NOT NULL REFERENCES prompts(id) ON DELETE CASCADE,
  label     TEXT NOT NULL DEFAULT '',
  url       TEXT NOT NULL,
  position  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX prompt_links_prompt ON prompt_links(prompt_id, position);

-- alias → kanonischer Tag-Slug (Slug statt ID, weil verwaiste Tags gelöscht werden)
CREATE TABLE tag_aliases (
  owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  alias    TEXT NOT NULL,
  tag_slug TEXT NOT NULL,
  PRIMARY KEY (owner_id, alias)
);

CREATE TABLE tag_rules (
  id         INTEGER PRIMARY KEY,
  owner_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  category   TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL CHECK (kind IN ('word','phrase','regex')),
  pattern    TEXT NOT NULL,
  tag_slug   TEXT NOT NULL,
  auto_apply INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX tag_rules_owner ON tag_rules(owner_id);

CREATE TABLE ml_models (
  owner_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL CHECK (kind IN ('tags','providers')),
  version    INTEGER NOT NULL,
  trained_at INTEGER NOT NULL,
  samples    INTEGER NOT NULL,
  quality    REAL,
  data       BLOB NOT NULL,
  PRIMARY KEY (owner_id, kind)
);

CREATE TABLE suggestion_feedback (
  owner_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  prompt_id  INTEGER NOT NULL REFERENCES prompts(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL CHECK (kind IN ('tag','provider')),
  value      TEXT NOT NULL,
  accepted   INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (owner_id, prompt_id, kind, value)
);

CREATE TABLE collections (
  id         INTEGER PRIMARY KEY,
  owner_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  notes      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX collections_owner ON collections(owner_id);

CREATE TABLE collection_prompts (
  collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  prompt_id     INTEGER NOT NULL REFERENCES prompts(id) ON DELETE CASCADE,
  added_at      INTEGER NOT NULL,
  PRIMARY KEY (collection_id, prompt_id)
);
CREATE INDEX collection_prompts_prompt ON collection_prompts(prompt_id);

-- zuletzt benutzte Werte für Platzhalter {{name}}
CREATE TABLE var_values (
  owner_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  prompt_id  INTEGER NOT NULL REFERENCES prompts(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  value      TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (owner_id, prompt_id, name)
);

-- Teilwortsuche („solo" findet „Gitarrensolo"), rowid = prompts.id
CREATE VIRTUAL TABLE prompt_trigram USING fts5(
  title, body,
  tokenize = "trigram remove_diacritics 1"
);
INSERT INTO prompt_trigram(rowid, title, body) SELECT rowid, title, body || ' ' || tags FROM prompt_fts;
