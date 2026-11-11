-- +goose Up
-- How tools authenticate the 1Password CLI. dex passes the credentials to tools and never reads
-- secrets itself. Mode '' is off; the stored tokens stay so that the user can turn it on again.
CREATE TABLE one_password (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    mode TEXT NOT NULL CHECK (mode IN ('', 'connect', 'service-account')),
    connect_host TEXT NOT NULL,
    connect_token TEXT NOT NULL,
    service_account_token TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE one_password;
