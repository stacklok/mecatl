# No timestamps, text, stack, endpoint, owner, ref, grant or arbitrary keys survive.
def member($values): . as $v | ($values | index($v)) != null;
def count: type == "number" and . >= 0 and . <= 1000000000000 and floor == .;
[inputs | fromjson? | select(type == "object")] as $rows |
($rows | map(select(.msg == "remote create stage" and .level == "DEBUG") |
  select(.stage | member(["http_handler","session_id_probe","bind_ensure","attach_poll","attach_poll_end","engine_factory","session_persist","reference_commit","http_response"])) |
  select(.reason | member(["begin","ok","error","cancelled","deadline","returned","pending","ready","not_ready_retryable","not_ready_nonretryable","remote_retryable","remote_terminal","transport_error"])) |
  select((.elapsed_ms | count) and (.calls | count)) |
  select(.session | type == "string" and test("^([a-f0-9]{32})?$"))) | .[-512:][] |
  {kind:"create_stage", source:$source, stage, reason, elapsed_ms, calls, session}),
{kind:"log_collection", source:$source, unavailable:any($rows[]; .collector_unavailable == true)},
(if any($rows[]; .collector_unavailable == true) then "" | halt_error(1) else empty end)
