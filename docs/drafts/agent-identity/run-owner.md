# Run owner and shared sessions

*This chapter describes the intended architecture, not current implementation status.*

Today one person owns a Mecatl session, and every credential the broker uses comes from that owner. Sessions shared by several people are a likely future direction. In a shared session, a tool call must use the credentials of the person who started the run, not the session owner's; otherwise Bob's run could read Alice's files. This chapter records the run owner now, in a way that lets shared sessions be added later without migrating data or changing token formats. It does not design shared sessions.

## The run owner

The **run owner** is the person who started a run. mecak8s records it when the run starts, from the company identity (`sub_id`) in the AS-1 token on the request that started the run. A delegated child run inherits its parent run's owner. While a session has one owner, the run owner always equals the session owner.

The identity work adds the run owner as follows:

1. **On every call.** mecak8s sends the run ID and the run owner with each `InvokeTool`, beside the session reference, operation ID, and agent credential. Both become part of the call's [execution context](mcp-authorization.md#authentication-and-execution-context).
2. **Checked, never used as a source.** The ID-JAG's `sub` still comes only from the record of the grant the broker uses. The broker checks that the reported run owner equals the owner recorded with that grant, the company identity, not AS-1's subject; today, that is the session owner. A mismatch refuses the call and is recorded as a denial. A run-owner report therefore cannot choose whose credentials are used. The check is a single function, so a later membership check replaces only its body.
3. **Grants keyed by session and user.** The broker and AS-1 store the session's broker refresh token under (session, user), with exactly one entry per session for now. The session's creator is recorded as a separate fact rather than as a uniqueness rule on the grant.
4. **The user in every per-user key.** The broker's cache of `V` and of ID-JAGs, and its store of vMCP session IDs, are keyed by user as well as session, instance, and gateway. The user comes from the grant's record, not from the reported run owner, and revoking a grant drops its entries. The vMCP already binds each session to the user.
5. **Revocation by user.** [Sign out everywhere](credentials-and-lifecycle.md#sign-out-and-revocation) revokes the user's grants and drops their cache entries. Closing a session is a separate action that revokes every grant in it.
6. **Through delegation.** Each [delegation event](audit.md#lineage) records the receiving run's owner, and the broker checks that a child run's owner equals its parent run's owner.
7. **In audit.** `Subjects` carries `run_owner_iss` and `run_owner_id` beside the session owner, marked as reported by mecak8s, together with the run ID.

A compromised mecak8s gains nothing from a false run owner: while one grant exists per session, the check only refuses calls.

## Shared sessions later

Shared sessions build on the run owner with additions:

| Shared sessions need | The run owner already provides | Shared sessions add or change |
| --- | --- | --- |
| Who started each run | Run ID and run owner on every call and in audit | Nothing |
| The run owner's credentials | Grants keyed by session and user | A grant for each member |
| No cross-user token reuse | Caches and vMCP sessions keyed by user | Nothing |
| Members other than the creator | The creator recorded separately | A member list kept by mecak8s; the owner check becomes a membership check |
| Revoking one member | Revocation by user | Nothing |

Shared sessions should arrive together with [run grants](credentials-and-lifecycle.md#later-work), not with a standing refresh token per member. With standing tokens, mecak8s's report of who started a run would select that member's token, so a compromised mecak8s could act as any member. A run grant is minted from the access token of the person who started the run, so no report from mecak8s decides whose credentials are used. A run grant could take the shape of a transaction token issued by AS-1 or the broker, whose `txn` and `sub` cannot change; it never reaches AS-2 or the vMCP.

Shared sessions also raise product questions outside identity:

- **One initiator per run.** If Alice interjects in Bob's run, that starts a new run or is refused.
- **Approvals.** Only the run owner approves permission prompts for their run.
- **Visibility.** What one member sees of another member's context, such as earlier tool results or documents.
- **Injection between members.** Content one member adds can steer a run another member starts, which then acts with the second member's credentials.
