-- +goose Up
-- Unify all inbound sources on the existing submissions table by separating
-- producer identity from optional direct-channel reply routing.
ALTER TABLE submissions ADD COLUMN producer TEXT NOT NULL DEFAULT 'user' CHECK (producer IN ('user', 'sub_session', 'scheduler', 'plugin'));
ALTER TABLE submissions ADD COLUMN reply_channel TEXT NOT NULL DEFAULT '';
ALTER TABLE submissions ADD COLUMN reply_peer TEXT NOT NULL DEFAULT '';

UPDATE submissions SET producer = CASE
    WHEN kind = 'message' AND origin = 'task' THEN 'sub_session'
    WHEN kind = 'message' AND origin IN ('automation', 'dream') THEN 'scheduler'
    WHEN kind = 'task_result' AND (
        result_child_id IS NOT NULL
        OR id LIKE 'result:%'
        OR id LIKE 'report:%'
        OR id LIKE 'err:%'
        OR id LIKE 'task:%:result'
    ) THEN 'sub_session'
    WHEN kind = 'task_result' AND (
        id LIKE 'monitor:%'
        OR id LIKE 'timer:%'
        OR id LIKE 'background:%'
        OR id LIKE 'routine:%'
    ) THEN 'scheduler'
    WHEN kind = 'task_result' AND id LIKE 'task-alert:%' THEN 'plugin'
    WHEN kind = 'task_result' THEN 'plugin'
    ELSE 'user'
END;

UPDATE submissions
SET reply_channel = substr(origin, 1, instr(origin, ':') - 1),
    reply_peer = substr(origin, instr(origin, ':') + 1)
WHERE producer = 'user'
    AND instr(origin, ':') > 1
    AND instr(origin, ':') < length(origin);

-- +goose Down
ALTER TABLE submissions DROP COLUMN reply_peer;
ALTER TABLE submissions DROP COLUMN reply_channel;
ALTER TABLE submissions DROP COLUMN producer;
