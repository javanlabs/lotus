-- +goose Up
-- Task 4.7 registered a third identity provider under the key "sso", and
-- this CHECK still listed only the two that existed when it was written.
-- The failure landed on a real user: clicking "Continue with SSO" on
-- production returned
--
--   new row for relation "oauth_states" violates check constraint
--   "oauth_states_provider_check" (SQLSTATE 23514)
--
-- before the browser ever reached the identity provider. Nothing in the
-- test suite covered it, because every test that exercises the OAuth state
-- table was written against github/google.
--
-- The list stays enumerated rather than becoming free text. "sso" is one
-- key covering every OIDC issuer, so this does not need editing per
-- customer IdP, and an enumerated column still catches a typo that would
-- otherwise sit in the database as an unusable state row.
ALTER TABLE oauth_states DROP CONSTRAINT oauth_states_provider_check;
ALTER TABLE oauth_states ADD CONSTRAINT oauth_states_provider_check
    CHECK (provider IN ('google', 'github', 'sso'));

-- +goose Down
ALTER TABLE oauth_states DROP CONSTRAINT oauth_states_provider_check;
ALTER TABLE oauth_states ADD CONSTRAINT oauth_states_provider_check
    CHECK (provider IN ('google', 'github'));
