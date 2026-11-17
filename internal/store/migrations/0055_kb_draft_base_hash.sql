-- A draft that replaces a published page names the file hash it was made from.
-- Publishing it when the file has another hash is a conflict, as an edit made
-- from a stale read is. Empty for a draft that creates a page.
ALTER TABLE kb_drafts ADD COLUMN base_hash TEXT NOT NULL DEFAULT '';
