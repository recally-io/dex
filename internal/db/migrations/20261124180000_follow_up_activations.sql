-- +goose Up
ALTER TABLE channel_follow_ups ADD COLUMN child_id INTEGER REFERENCES conversations(id);
ALTER TABLE channel_follow_ups ADD COLUMN parent_request_id TEXT REFERENCES submissions(id);
ALTER TABLE channel_follow_ups ADD COLUMN parent_source_key TEXT;
ALTER TABLE channel_follow_ups ADD COLUMN native_activation_id INTEGER;
ALTER TABLE channel_follow_ups ADD COLUMN staged_report TEXT;

UPDATE channel_follow_ups AS f
SET (child_id, parent_request_id, parent_source_key) = (
    SELECT turn.conversation_id, parent.id, parent.source_key
    FROM submissions turn JOIN submissions parent ON parent.id = turn.parent_request_id
    WHERE turn.id = f.delegation_id AND parent.conversation_id = f.conversation_id
);

-- A provider receipt takes precedence over terminal skip/revocation. It can
-- confirm part of an entry even when revocation prevents additional sends.
CREATE VIEW outbox_delivery_diagnostics AS
SELECT o.id, o.source_submission_id, o.peer, o.remote_ids,
    CASE
        WHEN o.peer <> '' AND EXISTS (
            SELECT 1 FROM json_each(o.remote_ids) r WHERE coalesce(r.value ->> 'id', '') <> ''
        ) THEN 'platform_confirmed'
        WHEN o.status = 'delivered' AND o.peer = '' AND json_array_length(o.remote_ids) = 0 THEN 'in_app_only'
        WHEN o.status = 'revoked' OR (o.status = 'delivered' AND o.skipped_part <> 0) THEN 'revoked_or_skipped_unconfirmed'
        ELSE 'unconfirmed'
    END AS disposition
FROM outbox_messages o;

-- +goose Down
DROP VIEW outbox_delivery_diagnostics;
ALTER TABLE channel_follow_ups DROP COLUMN staged_report;
ALTER TABLE channel_follow_ups DROP COLUMN native_activation_id;
ALTER TABLE channel_follow_ups DROP COLUMN parent_source_key;
ALTER TABLE channel_follow_ups DROP COLUMN parent_request_id;
ALTER TABLE channel_follow_ups DROP COLUMN child_id;
