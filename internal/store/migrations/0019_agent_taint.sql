-- A message carries outside_content when it holds text written by someone
-- outside the product: the result of a tool that reads third-party text, or a
-- system-made message built from such text. Taint follows the mark, so it
-- lives on the stored row and survives a restart and a copy by fork.
ALTER TABLE chat_messages ADD COLUMN outside_content INTEGER NOT NULL DEFAULT 0 CHECK (outside_content IN (0, 1));
