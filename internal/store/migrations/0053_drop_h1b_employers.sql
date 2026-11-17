-- Sponsorship is read from the posting's own words; the H-1B petition table
-- and its download are gone.
DROP TABLE IF EXISTS h1b_employers;
DELETE FROM data_sources WHERE kind = 'h1b';
