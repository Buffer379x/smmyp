-- SMMYP Schema v1 (0.1): Konten, Anbieter, Prompts (1..n Anbieter), Tags, Volltextindex.
-- Zeitstempel: Unix-Sekunden (UTC).

CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT,
  role          TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin','member')),
  settings      TEXT NOT NULL DEFAULT '{}',
  created_at    INTEGER NOT NULL,
  disabled_at   INTEGER
);

CREATE TABLE sessions (
  token_hash BLOB PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE invites (
  token_hash BLOB PRIMARY KEY,
  created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin','member')),
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_by    INTEGER REFERENCES users(id) ON DELETE SET NULL
);

CREATE TABLE providers (
  id            INTEGER PRIMARY KEY,
  owner_id      INTEGER REFERENCES users(id) ON DELETE CASCADE,  -- NULL = global
  slug          TEXT NOT NULL,
  name          TEXT NOT NULL,
  category      TEXT NOT NULL,
  color         TEXT NOT NULL,
  fields        TEXT NOT NULL,
  primary_field TEXT NOT NULL,
  sort          INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX providers_slug ON providers(ifnull(owner_id, 0), slug);

CREATE TABLE prompts (
  id           INTEGER PRIMARY KEY,
  owner_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title        TEXT NOT NULL,
  fields       TEXT NOT NULL,
  notes        TEXT NOT NULL DEFAULT '',
  model        TEXT NOT NULL DEFAULT '',
  is_favorite  INTEGER NOT NULL DEFAULT 0,
  use_count    INTEGER NOT NULL DEFAULT 0,
  last_used_at INTEGER,
  content_hash BLOB NOT NULL,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  deleted_at   INTEGER
);
CREATE INDEX prompts_recent ON prompts(owner_id, deleted_at, last_used_at DESC);
CREATE INDEX prompts_hash   ON prompts(owner_id, content_hash);

CREATE TABLE prompt_providers (
  prompt_id    INTEGER NOT NULL REFERENCES prompts(id) ON DELETE CASCADE,
  provider_id  INTEGER NOT NULL REFERENCES providers(id),
  owner_id     INTEGER NOT NULL,
  position     INTEGER NOT NULL DEFAULT 0,
  use_count    INTEGER NOT NULL DEFAULT 0,
  last_used_at INTEGER,
  PRIMARY KEY (prompt_id, provider_id)
);
CREATE UNIQUE INDEX prompt_providers_main   ON prompt_providers(prompt_id) WHERE position = 0;
CREATE INDEX        prompt_providers_drawer ON prompt_providers(owner_id, provider_id, last_used_at DESC);
CREATE INDEX        prompt_providers_prov   ON prompt_providers(provider_id);

CREATE TABLE tags (
  id       INTEGER PRIMARY KEY,
  owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name     TEXT NOT NULL,
  slug     TEXT NOT NULL,
  UNIQUE (owner_id, slug)
);

CREATE TABLE prompt_tags (
  prompt_id INTEGER NOT NULL REFERENCES prompts(id) ON DELETE CASCADE,
  tag_id    INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  source    TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','rule','similar','model')),
  PRIMARY KEY (prompt_id, tag_id)
);
CREATE INDEX prompt_tags_tag ON prompt_tags(tag_id);

-- rowid = prompts.id; wird von der App in derselben Transaktion gepflegt.
CREATE VIRTUAL TABLE prompt_fts USING fts5(
  title, body, tags, notes,
  tokenize = "unicode61 remove_diacritics 2",
  prefix   = '2 3'
);
