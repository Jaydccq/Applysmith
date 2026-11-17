-- Account creation needs no signed risk acknowledgement any more.
DROP TABLE IF EXISTS risk_acknowledgements;
DELETE FROM workspace_meta WHERE key = 'setup.accounts.declined';
