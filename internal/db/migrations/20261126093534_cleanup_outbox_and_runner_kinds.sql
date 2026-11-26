-- +goose Up
ALTER TABLE submissions DROP COLUMN delivered_at;

UPDATE runners SET kind = 'ssh' WHERE kind = 'pi-env';

ALTER TABLE channel_webhook_inbox RENAME TO channel_webhook_queue;

-- Keep existing message IDs because platforms can use them to deduplicate sends.
UPDATE outbox_messages SET topic_key = 'automation:' || substr(topic_key, 9)
WHERE kind = 'automation' AND topic_key GLOB 'routine:*';

-- +goose Down
UPDATE outbox_messages SET topic_key = 'routine:' || substr(topic_key, 12)
WHERE kind = 'automation' AND topic_key GLOB 'automation:*';

ALTER TABLE channel_webhook_queue RENAME TO channel_webhook_inbox;

UPDATE runners SET kind = 'pi-env' WHERE kind = 'ssh';

-- Restore the delivery marker from durable receipts; old timestamps without a receipt are lost.
ALTER TABLE submissions ADD COLUMN delivered_at INTEGER;
UPDATE submissions
SET delivered_at = coalesce(
    (SELECT max(o.delivered_at) FROM outbox_messages o
     WHERE o.source_submission_id = submissions.id AND o.kind IN ('reply', 'question') AND o.status = 'sent'),
    CASE WHEN reason = 'withdrawn' THEN settled_at END
);
