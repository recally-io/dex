-- +goose Up
ALTER TABLE submissions ADD COLUMN result_child_id INTEGER REFERENCES conversations (id);
ALTER TABLE submissions ADD COLUMN native_answer_id INTEGER;
CREATE UNIQUE INDEX submissions_child_answer ON submissions (result_child_id, native_answer_id);

-- +goose Down
DROP INDEX submissions_child_answer;
ALTER TABLE submissions DROP COLUMN native_answer_id;
ALTER TABLE submissions DROP COLUMN result_child_id;
