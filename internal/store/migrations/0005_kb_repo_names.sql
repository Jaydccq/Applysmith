-- Repository names are case-insensitive on GitHub, so one lower-case form is
-- stored and a case-insensitive unique index guards it. Rows that differ only
-- in case are merged into the oldest one, and the drafts of a merged row move
-- to it.

UPDATE kb_drafts SET repo_id = (
    SELECT min(r2.id) FROM kb_repos r1
    JOIN kb_repos r2 ON lower(r1.owner) = lower(r2.owner) AND lower(r1.name) = lower(r2.name)
    WHERE r1.id = kb_drafts.repo_id)
WHERE repo_id IN (SELECT id FROM kb_repos);

DELETE FROM kb_repos WHERE id NOT IN (SELECT min(id) FROM kb_repos GROUP BY lower(owner), lower(name));

UPDATE kb_repos SET owner = lower(owner), name = lower(name);

CREATE UNIQUE INDEX kb_repos_names ON kb_repos (lower(owner), lower(name));
