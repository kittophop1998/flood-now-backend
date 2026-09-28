-- Sessions move from a bearer token kept in the browser's localStorage to an
-- HttpOnly cookie (see docs/api-spec.md#accounts-auth). Tokens issued before
-- this were readable by page scripts, and the API no longer accepts them the
-- old way; revoke them all so every live session is a cookie session.
-- Users simply sign in again. The table itself is unchanged: it already
-- stores only the SHA-256 of each token.
DELETE FROM user_sessions;
