# Shopkeet — AI Development Rules (multi-agent "software house" model)

Shopkeet will be built by multiple AI agents working in parallel, the way a small software house splits work across specialists rather than one generalist doing everything sequentially. This document defines the roles and the ground rules that keep their work coherent — the same job a tech lead does for a human team.

## Roles

Not every project needs every role active at once, but this is the division of responsibility:

| Role | Owns | Works from |
|---|---|---|
| **Architect / Tech Lead agent** | Final call on architecture decisions; resolves ambiguity; approves any deviation from documented decisions | `01`–`06` docs, all of them |
| **Backend agent** | Go API implementation | `04-agent-build-spec.md`, one phase at a time |
| **Frontend agent** | Next.js implementation | `05-frontend-agent-spec.md` + the API contract from `04` |
| **QA / Test agent** | Verifies each phase's acceptance criteria independently before it's marked done | `04`/`05` acceptance criteria — should not be the same agent that built the feature |
| **DevOps / Infra agent** | `docker-compose`, CI/CD, migrations, environment config | `02-tech-stack.md`, `04` §1–2 |

A single agent can hold multiple roles on a small build, but should still *switch hats* deliberately — e.g., don't let backend-implementation momentum carry into approving your own architecture deviation.

## Ground rules — apply to every agent, every role

1. **The docs are the source of truth, not memory of the conversation that produced them.** Before writing code for a feature, (re-)read the relevant doc section. If a request conflicts with something documented, that's a flag to raise, not a doc to silently override.

2. **No new dependency, service, or infrastructure piece outside `02-tech-stack.md` without flagging it first.** This is the rule that keeps "just add Kafka, it'll help" from creeping back in one small decision at a time across many agents and many sessions.

3. **Stay inside scope.** `01-mission.md`'s "What we are explicitly NOT building" list is a real boundary, not a suggestion — building toward it anyway (a plugin marketplace, multi-warehouse logic) because it seemed useful is scope creep, not initiative.

4. **One phase at a time, gated by passing acceptance criteria — not by an agent's own claim of completion.** Phase N+1 doesn't start until Phase N's criteria are demonstrated by a passing automated test, ideally checked by the QA role rather than self-certified.

5. **A change to any contract between modules — API shape, event name, schema, block type — updates the relevant doc in the same change.** Two agents working from a stale doc is how backend and frontend silently drift apart. The doc update is part of the change, not a follow-up task.

6. **Follow existing conventions rather than introducing new ones.** The "Conventions" sections in `04` and `05` (Go structure, migration style, commit style, component organization) apply to whichever agent is writing the code that day — consistency across the codebase matters more than any individual agent's preferred style.

7. **Every acceptance criterion has a corresponding automated test before a phase counts as done.** "It works when I tried it" is not a test.

8. **No secrets committed, ever.** Real values live in environment/secret storage; only `.env.example` (with placeholder values) is committed.

9. **When a spec is ambiguous, surface the gap — don't silently resolve it and move on.** A comment, a PR note, or a direct question is cheap. A quietly-invented decision that contradicts what another agent assumes is expensive to untangle later.

10. **Keep units of work small and reviewable.** One phase's work should land as a small number of focused PRs, each reviewable on its own — not one enormous commit at the end of a phase.

## Coordination protocol

Backend and Frontend agents work in parallel against the **documented API contract** in `04`, not against each other's in-progress code — this is what lets them move independently without blocking on each other. If the Backend agent needs to change an endpoint's shape mid-build, it updates `04` first; the Frontend agent then picks up that change deliberately, rather than discovering the API silently moved underneath it.

"Definition of done" for a unit of work is the same regardless of which agent did it: tests pass, relevant docs are updated, nothing outside the documented scope was added. That's the actual merge gate — not any individual agent's confidence that it's finished.

## Escalate to a human instead of deciding alone, when:

- A task seems to require breaking one of the "Non-negotiable constraints" in `04` §0.
- A request would build something on `01-mission.md`'s out-of-scope list.
- Two docs (or a doc and a request) genuinely conflict and there's no clear resolution from reading them.
- An agent notices another agent's completed work doesn't match its acceptance criteria — raise it rather than quietly reworking it.

These are the moments where an agent guessing and continuing is more expensive than a short pause to ask.
