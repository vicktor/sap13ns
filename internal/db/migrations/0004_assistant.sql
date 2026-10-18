-- Site assistant: visitors ask questions answered from the site's own public
-- posts and pages. The index is a set of Markdown-aware chunks per post with
-- a full-text vector; semantic search adds an embedding column at runtime.
--
-- pgvector is deliberately NOT created here: it is not a trusted extension,
-- so a superuser (the DBA) has to run CREATE EXTENSION vector SCHEMA public.
-- When it is available, the server adds assistant_chunks.embedding with the
-- dimension of the configured model (vector(N), or halfvec(N) above 2000)
-- and an HNSW cosine index. Without it, the assistant uses full-text search.

-- Lets an editor keep a post out of the assistant (and the MCP endpoint).
ALTER TABLE posts ADD COLUMN assistant_exclude boolean NOT NULL DEFAULT false;

-- Retrieval units. heading is the post title plus the heading path of the
-- section ("Title › Install › Requirements"); content is the section text.
-- What gets embedded is heading + blank line + content, and content_hash is
-- the SHA-256 of exactly that, so unchanged chunks keep their embedding.
-- The text search configuration follows the chunk language (spanish,
-- english, otherwise simple); queries use the same mapping.
CREATE TABLE assistant_chunks (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    post_id       bigint NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    lang          text NOT NULL,
    chunk_no      integer NOT NULL CHECK (chunk_no >= 0),
    heading       text NOT NULL DEFAULT '',
    content       text NOT NULL,
    tsv           tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector(CASE split_part(lang, '-', 1)
            WHEN 'es' THEN 'spanish'::regconfig WHEN 'en' THEN 'english'::regconfig ELSE 'simple'::regconfig END,
            heading), 'A') ||
        setweight(to_tsvector(CASE split_part(lang, '-', 1)
            WHEN 'es' THEN 'spanish'::regconfig WHEN 'en' THEN 'english'::regconfig ELSE 'simple'::regconfig END,
            content), 'B')
    ) STORED,
    content_hash  text NOT NULL,
    model         text NOT NULL DEFAULT '', -- embedding model; '' while not embedded
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (post_id, chunk_no)
);
CREATE INDEX assistant_chunks_tsv_idx ON assistant_chunks USING gin (tsv);

-- One row per indexed post: the version that was indexed, with which
-- embedding model, and the last error (failed posts are retried with a
-- growing delay).
CREATE TABLE assistant_docs (
    post_id         bigint PRIMARY KEY REFERENCES posts (id) ON DELETE CASCADE,
    source_hash     text NOT NULL,
    post_updated_at timestamptz NOT NULL,
    model           text NOT NULL DEFAULT '',
    chunks          integer NOT NULL DEFAULT 0,
    error           text NOT NULL DEFAULT '' CHECK (length(error) <= 500),
    attempts        integer NOT NULL DEFAULT 0,
    indexed_at      timestamptz NOT NULL DEFAULT now()
);

-- Site-wide daily usage, for the budget and the admin report.
CREATE TABLE assistant_usage (
    day           date PRIMARY KEY,
    requests      integer NOT NULL DEFAULT 0,
    input_tokens  bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    embeddings    integer NOT NULL DEFAULT 0
);
