-- Both inputs only request evaluation. The rule reads the committed answer
-- and issue status before dispatch, including when the answer arrives last.
CREATE FUNCTION hint_member_choice_rules() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE watched uuid;
BEGIN
 IF TG_TABLE_NAME='issue' THEN watched:=NEW.id; ELSE watched:=NEW.issue_id; END IF;
 INSERT INTO issue_wakeup_receipt(id,wakeup_id,revision,event_key,event_type,payload,coalesce_key)
 SELECT gen_random_uuid(),w.id,w.revision,gen_random_uuid()::text,'member_choice.changed','{}'::jsonb,'member_choice.changed'
 FROM issue_wakeup w WHERE w.workspace_id=NEW.workspace_id AND w.enabled AND w.condition ? 'member_option'
 AND w.condition->>'issue_id'=watched::text ORDER BY w.id
 ON CONFLICT (wakeup_id,revision,coalesce_key) WHERE processed_at IS NULL AND coalesce_key IS NOT NULL DO NOTHING;
 RETURN NULL;
END $$;

CREATE TRIGGER hint_member_choice_status AFTER UPDATE OF status ON issue FOR EACH ROW
 WHEN (OLD.status IS DISTINCT FROM NEW.status AND NEW.metadata ? 'member_todo_key') EXECUTE FUNCTION hint_member_choice_rules();

CREATE TRIGGER hint_member_choice_answer AFTER INSERT ON comment FOR EACH ROW
 WHEN (NEW.author_type='member' AND strpos(NEW.content,'multica-member-answer')>0) EXECUTE FUNCTION hint_member_choice_rules();
