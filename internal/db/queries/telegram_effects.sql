-- name: GetTelegramEffect :one
SELECT * FROM telegram_effects WHERE operation_key=?;

-- name: CreateTelegramEffect :execrows
INSERT INTO telegram_effects(operation_key,agent_id,account_id,sender_id,peer,source_key,kind,input_json,state,created_at)
SELECT @operation_key,a.id,@account_id,@sender_id,@peer,@source_key,@kind,@input_json,'ready',@created_at
FROM agents a JOIN telegram_settings t ON t.agent_id=a.id
WHERE a.id=@agent_id AND a.archived_at IS NULL
AND substr(t.token,1,instr(t.token,':')-1)=@account_id
AND instr(','||replace(t.allowed_users,' ','')||',',','||@sender_id||',')>0
ON CONFLICT DO NOTHING;

-- name: StartTelegramEffect :execrows
UPDATE telegram_effects SET state='processing' WHERE operation_key=? AND state='ready';

-- name: CompleteTelegramEffect :execrows
UPDATE telegram_effects SET state=@state,text=@text,body=@body,name=@name,result_json=@result_json,error=@error
WHERE operation_key=@operation_key AND state='processing';

-- name: MarkTelegramEffectUnknown :exec
UPDATE telegram_effects SET state='unknown',error='remote outcome unknown; automatic retry is disabled'
WHERE operation_key=? AND state='processing';

-- name: ClearTelegramEffectInput :exec
UPDATE telegram_effects SET input_json='{}',body=NULL WHERE operation_key=?;

-- name: ListTelegramPeerReplies :many
SELECT s.id,s.answer,s.rendered_reply,s.channel_remote_ids,s.channel_context
FROM submissions s JOIN conversations c ON c.id=s.conversation_id
WHERE c.agent_id=? AND s.origin=? AND s.delivered_at IS NOT NULL AND s.channel_revoked=0
AND s.channel_remote_ids<>'[]' ORDER BY s.rowid DESC LIMIT 20;

-- name: ListDiscardedTelegramEffectInputs :many
SELECT operation_key,input_json,source_key FROM telegram_effects
WHERE agent_id=? AND state='discarded' AND input_json<>'{}';

-- name: DiscardTelegramInputEffects :exec
UPDATE telegram_effects SET state='discarded',body=NULL
WHERE agent_id=? AND (input_json<>'{}' OR body IS NOT NULL OR state IN ('ready','processing'));

-- name: ListTelegramPeerEffects :many
SELECT operation_key,result_json FROM telegram_effects
WHERE agent_id=? AND peer=? AND account_id=? AND sender_id=? AND kind IN ('copy','forward') AND state='done'
ORDER BY rowid DESC LIMIT 20;
