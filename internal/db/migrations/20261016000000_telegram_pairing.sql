-- +goose Up
ALTER TABLE telegram_settings ADD COLUMN pairing_code TEXT NOT NULL DEFAULT '';
ALTER TABLE telegram_settings ADD COLUMN pairing_expires INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE telegram_settings DROP COLUMN pairing_expires;
ALTER TABLE telegram_settings DROP COLUMN pairing_code;
