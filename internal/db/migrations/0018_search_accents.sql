-- Search ignores accents: "mediacion" finds "Mediación" and "mediación"
-- finds "mediacion". The column folds the text as the queries fold what
-- is searched (store.searchFold, which must match): lower case, then the
-- accented Latin letters to plain ones. translate() needs no extension;
-- unaccent would, and a site's database role may not be allowed to create
-- one. Dropping the column drops its index too.
ALTER TABLE posts DROP COLUMN search;
ALTER TABLE posts ADD COLUMN search tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', translate(lower(coalesce(title, '')), 'áàâäãåéèêëíìîïóòôöõúùûüýÿñç', 'aaaaaaeeeeiiiiooooouuuuyync')), 'A') ||
    setweight(to_tsvector('simple', translate(lower(coalesce(excerpt, '')), 'áàâäãåéèêëíìîïóòôöõúùûüýÿñç', 'aaaaaaeeeeiiiiooooouuuuyync')), 'B') ||
    setweight(to_tsvector('simple', translate(lower(coalesce(content, '')), 'áàâäãåéèêëíìîïóòôöõúùûüýÿñç', 'aaaaaaeeeeiiiiooooouuuuyync')), 'C')
) STORED;
CREATE INDEX posts_search_idx ON posts USING gin (search);
