-- +goose Up
CREATE INDEX outbox_messages_timeline ON outbox_messages (conversation_id)
    WHERE kind = 'proactive';

-- +goose Down
DROP INDEX outbox_messages_timeline;
