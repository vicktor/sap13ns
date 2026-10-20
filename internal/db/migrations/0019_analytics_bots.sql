-- Automated traffic, kept apart from the visits of people: daily hits per
-- bot category (search, ai, social, seo, other, scanner) and bot name
-- ("GPTBot", "curl"; '' for scanners). Like the other analytics tables it
-- holds aggregates only, never an address or a User-Agent.
CREATE TABLE analytics_bots (
    day      date NOT NULL,
    category text NOT NULL CHECK (category IN ('search', 'ai', 'social', 'seo', 'other', 'scanner')),
    name     text NOT NULL CHECK (length(name) <= 64),
    hits     bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (day, category, name)
);
