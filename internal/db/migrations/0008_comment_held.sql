-- Which comment.check plugin held a comment or marked it as spam, and why,
-- for the moderation queue.
ALTER TABLE comments
    ADD COLUMN held_by text NOT NULL DEFAULT '' CHECK (length(held_by) <= 64),
    ADD COLUMN held_reason text NOT NULL DEFAULT '' CHECK (length(held_reason) <= 500);
