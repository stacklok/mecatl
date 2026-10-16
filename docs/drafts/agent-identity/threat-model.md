# Threat model

*This chapter describes the intended architecture, not current implementation status.*

This chapter lists the attackers the design considers, what each can do, and which protections depend on where the broker runs. The [overview](README.md#deployment-and-threats) summarizes it.

## Deployment

The broker starts as modules inside the mecak8s process: broker core, AS-1, the agent-instance issuer, and the definition loader. Later these modules move together into one separate broker service. The protocol and token formats are the same in both deployments; what changes is which process holds the broker's keys, refresh tokens, and records. Starting inside mecak8s ships the protocol sooner. Moving out protects those keys, tokens, and records against a compromised mecak8s; it is not about hostile workspace code, which executor Pods already contain.

## Protections in both deployments

Some protections do not depend on where the broker runs:

- **Hostile workspace code** runs in executor Pods, behind the execution provider. In both deployments, executors get no broker credentials, no database credentials, and no network path to mecak8s internals or the key management service (KMS).
- **A prompt-injected agent** never sees or chooses credentials. Harness code attaches them, and the model supplies only tool names and arguments.
- **The user comes from a sign-in at AS-1**, not from a mecak8s report. mecak8s passes on the AS-1-signed access token once, AS-1 converts each token at most once, and every ID-JAG names the user from AS-1's record. Correct mecak8s code cannot name a user who did not sign in. An attacker running code in the process that holds AS-1's keys can, and while the broker runs inside mecak8s that process is mecak8s; see the first row of the table below.

## A compromised mecak8s

Where the broker runs matters against one attacker: someone with code execution in the mecak8s process. The table shows, for each threat, whether the attack works, is limited, or is blocked.

| Threat from a compromised mecak8s | Broker inside mecak8s | Separate broker service |
| --- | --- | --- |
| Act as any user, by signing an ID-JAG for a user who is not present | Attack works. The process holds AS-1's signing key and the broker's client key. In embedded mode, the attacker can reach every user connected to the gateway. In external mode, the attacker can reach only users who have a consent link. | Attack limited. mecak8s holds no keys. It can act only in broker sessions that are open and bound to its workload. A session ends when it closes, when its refresh token's hard expiry passes, when the user signs out everywhere, or when AS-1's upstream re-check revokes the user's grants. mecak8s can also open new sessions with fresh tokens from users who are using it now, and, through their schedule grants, sessions for any user with a live schedule, until each schedule's horizon. |
| Steal refresh tokens or signing keys | Attack works. A KMS stops the attacker from copying a key out. It does not stop the attacker from signing with the key while the attacker is inside the process. | Attack blocked, if the broker has its own database credentials and only the broker's service account can sign with its KMS keys. |
| Read users' provider tokens in embedded mode | Attack works, if the gateway store shares storage credentials with mecak8s. | Attack blocked, if the embedded gateway is its own deployment with its own credentials. |
| Tamper with broker and AS-1 audit records | Attack partly blocked. Records that are already in an external append-only store survive. From then on, the compromised process can forge or suppress its own records. | Attack blocked for broker and AS-1 records, because mecak8s does not write them. This holds if the records go to an append-only store that mecak8s cannot write. |
| Report a false agent, parent, run, or root | Attack works. An attacker in the process can bypass the broker's checks and its records. | Attack limited. The report still cannot be verified, but it must pass the broker's consistency checks, is recorded where mecak8s cannot erase it, and must name a root-eligible definition. Once the authority work is in place, it cannot exceed the widest root-eligible ceiling. |

## What mecak8s reports

In both deployments, the broker trusts mecak8s to report these facts:

1. Which agent instance made a call.
2. Which loaded definition a session uses as its root.
3. Which instance created which child.
4. The run and delegation context.
5. Which open session a call belongs to.

Only mecak8s observes these facts: the agents are goroutines in its memory, and nothing outside the process can see which one is which. The broker cannot verify them; it checks that they are consistent. The acting instance must be registered to the session, a parent must belong to the same session tree and not be revoked, a delegation's links must line up, the run owner must match the grant in use, and a session's root must be a definition the operator made root-eligible. For fact 5, the broker checks the session against the user's token and the session's recorded owner. These checks catch mistakes, not a compromised mecak8s, which can report a consistent lie. The broker loads global definitions itself, so mecak8s cannot supply their content or ceilings, and audit records mark each fact that mecak8s reported.

A separate broker does not make these facts verifiable, but it makes its checks impossible to skip and records every report where mecak8s cannot erase it. Making them verifiable would need each agent in its own pod or sandbox, with attestation, which is outside this design.

The [authority work](README.md#order-of-work) limits what those reports can achieve. With ceilings set by the loader and capped by root eligibility, a compromised mecak8s cannot exceed the widest definition a session may use as its root, whichever agent it claims is acting. A run grant is a credential that AS-1 mints for one run, from a fresh access token the user sent. With run grants and a separate broker, a compromised mecak8s needs a fresh token from the user for each run, but it can still turn any request the user sends into a run of its choosing, one run per token, and scheduled runs keep their own authority. Only a key held by the client that signs each run's start would prevent that.

## What the split requires

A separate broker service delivers its protections only if the split is real:

- the broker has its own database, KMS permissions, and namespace;
- mecak8s cannot create pods, read secrets, or change configuration in the broker's namespace;
- mecak8s cannot write the broker's workload allowlist, trust bundle, or policy inputs;
- the embedded gateway is its own deployment.

While the broker runs inside mecak8s, the [modulith constraints](implementation.md#modulith-constraints) keep the later move a deployment change.
