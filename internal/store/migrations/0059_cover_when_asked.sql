-- What a run does when an application form asks for a cover letter: write one
-- whatever the posting scored ('always', the owner's default), or only for a
-- posting whose documents clear the score floor ('eligible').
ALTER TABLE document_preferences ADD COLUMN cover_when_asked TEXT NOT NULL DEFAULT 'always' CHECK (cover_when_asked IN ('always', 'eligible'));
