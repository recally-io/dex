-- +goose Up
-- The access token that the API requires on a non-loopback host. dex creates it on start, so it
-- exists before the user changes the host. DEX_TOKEN replaces it for a foreground process.
CREATE TABLE access_token (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE access_token;
