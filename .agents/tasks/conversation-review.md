# Conversation Review

Interactive review of one finished or stopped hotseat conversation and the logs around it. Finds what caused friction in the hotseat process or broke communication between participants, traces each issue to the text or code that caused it, then walks through each one and applies or tickets the resolution.

Goal: catch issues that will recur on the next run. A finding names something a future conversation would hit again: member-facing text that misleads, a `run` mechanic that wastes launches or stops wrongly, a tool that fails or prompts, or a communication pattern the roles and tasks allow and should not. A one-off model slip with no cause in the text, the code, or the environment is noted, not fixed.

Finding no issues is a valid outcome. A clean conversation is evidence the roles, tasks, and conductor are working. Say so rather than invent findings to justify the review.

## Inputs

The material is appended at the bottom of these instructions. Expect some or all of:

- The conversation: `hotseat read` or `hotseat status` text, with `seq`, `from`, `to`, `kind`, `re`, and `body`. A multi-line value is `key <<N` followed by N bytes
- `hotseat member list` rows: launched time, registered time, and status per member
- The bus log, `hotseat.log`: one `call` line per operation with `op`, `conversation`, `outcome`, `reason` on a refusal, and `duration`
- `hotseat run` stdout (the header and result block) and stderr: `--verbose` event lines, and `start` and `tk` stderr with the tool name prefixed
- Agent session logs for each member and for the chief: the prompt it received, its tool calls, their output, and what it published

Before reviewing, list which of these are present. A missing input limits what can be found. Name the limit in the report and do not guess what the missing log would have shown.

Order everything by time. A message's `seq` is commit order within the store. Line up each message with the bus log call that stored it, the `run` event that launched its sender, and the agent log turn that issued the publish.

The bus is read-only for this review. Do not run `hotseat publish`, `hotseat run`, `hotseat member register`, `hotseat bus stop`, or anything else that writes to the bus or the store. Read-only commands (`hotseat status`, `hotseat member list`, `tk design get`) are allowed only when a finding turns on a fact the material does not hold.

Note: session ids, pids, member start tokens, the bus token, and lease holders never go into the report, a finding, a ticket, or an edit. Refer to a member by roster name and to a message by `seq`.

## Reference

Read these before judging behaviour. A member is judged against the text it was given, not against what the reviewer thinks it should have done.

| Source | Holds |
| --- | --- |
| `../library/roles/hotseat/member/` | Member protocol, kinds, replies, findings, acting seat |
| `../library/roles/hotseat/chief/` | Chief protocol: run, attend, status, answering `ask` |
| `../library/contexts/hotseat/bus/` | Client commands and kinds |
| `../library/tasks/hotseat/<task>/` | The task the conversation ran: phases, templates, report format |
| `../library/agents/hotseat/claude/`, `../library/agents/hotseat/grok/` | How `start` launches each member: permission, effort, output, resume |
| `README.md` | Bus, client, `run`, and `attend` behaviour, including every `error` class and stop cause |
| `AGENTS.md` | Invariants and the runner file map |
| `internal/run/` | The prompts `run` writes and the rules it enforces |

The task is the conversation's `task` binding. The run's prompts are in the agent logs. When the logs lack them, read them from `internal/run/` and say so.

## Workflow

### Phase 1: Review

1. Inventory the inputs as above. Identify the conversation name, task, ticket, roster, seat, and how the run stopped: the `stop` and `outcome` lines, or the stopping `done`
2. Read the reference text for that task and the member and chief roles
3. Reconstruct the run as a timeline: each `turn` and its set, each launch and its exit status, each finding and its discussion, each `settled`, `fixed`, `error`, `ask`, and the `done`. Note wall-clock gaps between steps
4. Walk the timeline against the Friction Signals below. For each candidate, find the evidence in at least one log and the cause in the reference text, the code, or the environment
5. Identify concerns that meet the Goal bar. Apply the Articulation Test and Regret Filter before listing each one
6. Produce the report in the Report Format and present it inline. Do not write a report file unless Save applies
7. With no actionable findings, skip to Phase 4. Otherwise display the Top-level Prompt

### Friction Signals

Protocol and conductor:

- A protocol `error` of any class: `no reply`, `bad turn`, `bad done`, `bad finding`, `no stance`, `extra stance`, `bad stance`, `bad pass`, `bad extend`, `bad deadlock`, `no aggregate`, `bad aggregate`, `no decision`, `no fix`, `no report`, `unsettled`
- A refused publish in the bus log, and its `reason`
- A wrong or missing `--re`, a message addressed to `all`, or a finding not addressed to every roster name
- A member that ran `hotseat wait`, read with the wrong filter, or never registered
- A launch that registered and exited without publishing what its prompt asked for
- A `timed-out` member, a backup handoff, or a `running` row far longer than its peers
- A stall resolved by `extend` that changed nothing, or a `deadlock` the material shows was avoidable
- A launch `run` started that the transcript did not need, or a step it skipped that the task requires
- A stop whose cause or outcome does not match what the transcript shows

Communication:

- A finding that fails its task's Finding Template bar: a reader cannot restate it after one read, it has no proposal, or its body is only a `seq`
- Duplicate findings the aggregate missed, or distinct findings it merged
- A stance that does not engage the current proposal, or two members talking past each other
- An `object` with no reason a proposer could act on, or a `stand` that repeats the proposal without answering the objection
- A `pass` that asks for no specific answer
- An `ask` that needed a blocking answer but was raised during a discussion, where the answer arrives too late
- A `done` report whose Remediation Summary disagrees with the settles and fixes in the transcript
- A `fixed` whose body does not say what changed, or that applied something other than the settled proposal
- Speech that is `run`'s bookkeeping, or a conversation a reader who was not there could not follow

Tooling and environment, from `run` stderr and the agent logs:

- A command that failed: `start`, `tk`, `hotseat`, `git`, a build, or a test
- A permission prompt or denial that stalled a non-interactive member
- A flag the member guessed wrong, or a command it retried with variations
- Long exploration before the first publish, repeated reads of the same material, or a member re-deriving context the prompt should have given
- A contradiction between the role, the context, the task, and the run prompt that a member had to resolve by guessing

### Phase 2: Remediation

Every finding is a decision. Nothing is applied before its walk.

Let `m` be the count of actionable findings. The top-level choice selects how they are handled: `C` walks them one at a time, `A` applies the recommended resolution to every finding. Letters are case-insensitive.

Continue (`C`) walks the findings one at a time. For each finding:

1. Re-read the evidence and the reference text it cites. Re-evaluate the finding. An earlier fix may have covered it. If it no longer holds, say so and withdraw or revise it before presenting
2. Lock the Recommendation: does it remove the cause in the owning text or code, or does it patch the symptom in one task? Name the cheapest alternative and reject it only if it is worse on maintenance or correctness, not effort. If the finding needs the transcript in the reader's head to make sense, rewrite it until it does not, or withdraw it
3. Present the finding using the Per-item Template, with `n` its position and `m` the total
4. Display the Per-item Prompt and pause for an explicit decision. Accepting one finding does not authorise the next. If a response is ambiguous, ask which finding it applies to

Per-item commands. Outcomes are tracked in-session and surface in the Phase 4 summary:

- An option letter (`A`, `B`, `C` …): apply that option. Track as `Fixed`
- `R`: apply exactly what the Recommendation states. Track as `Fixed`
- `N`: move to the next finding. Track as `Skipped`
- `T`: create a tk ticket for this finding (see Ticket). Track as `Ticket: <id>`
- `S`: see Save

All (`A`) works through the findings in order without the Per-item Prompt. For each: announce `(n of m) <short title>`, re-evaluate and lock the Recommendation as in the walk, then apply it. A finding that no longer holds is `Skipped`. A recommendation of `T` creates the ticket. The edit is the checkpoint: if an edit is denied, stop and discuss that finding, then resume.

Where a resolution lands depends on the owner:

| Owner | Resolution |
| --- | --- |
| Member-facing text: `roles/hotseat/*`, `contexts/hotseat/*`, `tasks/hotseat/*`, `agents/hotseat/*` in `../library` | Edit in place in `../library`. Keep each role, context, and task consistent with the others that state the same rule. Run the validation `../library/AGENTS.md` gives. Do not commit |
| `hotseat` code, `README.md`, or `--help` | `T` in scope `hotseat`. Code changes are implemented and tested in their own session, not in this review |
| `start`, `tk`, or another p3bot tool | `T` in that tool's tk scope. Ask for the scope when it is not clear |
| Model behaviour with no cause in text, code, or environment | No change. Track as `Noted`, and say which model and why no text would have prevented it |

Remediation guidance:

- Fix the cause, not the conversation. A rule a member broke because two texts disagree is fixed by making them agree, not by restating the rule a third time
- A rule `run` enforces belongs in `run`'s prompt or code, and a judgement members make belongs in the role or task. Do not move mechanics into member text to work around a conductor gap
- Put a rule in the one text that owns it. A rule every task needs belongs in the member role, not copied into each task
- Prefer removing a confusing sentence to adding a clarifying one
- When an edit is too large or touches several modules, recommend `T`

### Phase 3: Satisfaction Pass

After every finding is processed, re-read each library file this review edited. Surface only what the edits introduced: a rule now stated two ways, a reference to removed text, a contradiction with a section left untouched, or drift between the member role and the bus context. This is not a second review. Handle new findings in the mode chosen at the top level.

### Phase 4: Wrap-up

1. Declare the outcome:
   - Clean: no friction worth fixing
   - Resolved: every finding is `Fixed`, `Ticket`, or `Noted`
   - Issues to resolve: findings remain `Skipped` or `Pending`. List them by number and title
2. Print the Remediation Summary
3. List the library files edited, so the owner can review and commit them

## Reviewer Guidance

- Judge against the text the participant was given. A member that followed a misleading instruction did not fail; the instruction did
- Evidence bar: every finding cites at least one `seq`, bus log line, `run` event, or agent log turn. A finding with no evidence does not belong in the list
- Recurrence bar: flag a finding only if the next run of the same or another task would hit it again
- Articulation Test: if you cannot say what goes wrong on the next run when the item is left, drop it
- Regret Filter: would you regret not flagging this after ten more runs? If not, drop it
- Count the cost: launches, turns, wall time, or a wrong outcome. A finding that costs nothing measurable is a nit and is dropped
- One cause, one finding. Several symptoms of one cause are one finding with every symptom as evidence
- Permission to find nothing

## Issue Categories

| Category | Use when |
| --- | --- |
| protocol | A participant broke a bus or finding-protocol rule, and the cause is in the text it was given |
| conductor | `run` launched, judged, stopped, or reported wrongly or wastefully |
| communication | A message failed its reader: a finding, stance, pass, ask, or report that could not be acted on after one read |
| prompt | Role, context, task, or run prompt text is ambiguous, contradictory, missing, or misleading |
| tooling | A command, flag, permission, or environment fact failed or slowed a participant |
| decision | The owner needs to choose between valid alternatives |

## Per-item Template

A finding is read by someone who was not in the conversation, has not opened the logs, and has to decide after one read. The bar: that reader can restate the problem in their own words.

Open with the smallest concrete instance, then explain it. Four moves, in order:

1. Show what should have happened and what did. Quote the message, the log line, or the agent's command and output, trimmed to the lines that show the break, with the expected behaviour alongside
2. Give the Simple Explanation: one or two sentences in everyday language
3. Say what causes it: the sentence in the role, task, or prompt, or the code path, quoted with its file
4. Say what it costs on this run and on the next

Writing rules:

- Name things by what they are. "the member role's Replies section", then `roles/hotseat/member/role.md` as the anchor
- Spell out hotseat shorthand on first use. Not every reader knows a stance, a set, or the acting seat
- Never fabricate an observable. Quote only what the material holds
- Separate each option with a blank line

```
### Finding n of m: <short title>

Category: <protocol | conductor | communication | prompt | tooling | decision>
Owner: <library file, hotseat package, tool, or model>
Evidence: <seq, log line, or agent log turn>

<the smallest concrete instance: what happened, with what should have happened alongside>

Simple Explanation

<one or two sentences naming the problem>

Details

<continuous prose: the cause, quoted with its file, and the cost>

Decision

<the single question for the reader>

Options

A. <option: what it does, its tradeoff>

B. <option>

Recommendation (<letter>)

<one clause on why it removes the cause>
```

Include Options only when alternatives clarify the choice. Otherwise lead with a single Recommendation that `R` accepts. A `Noted` finding has no Decision, Options, or Recommendation.

## Report Format

```
## Conversation Review

Conversation: <name>
Task: <task>
Ticket: <id, or none>
Roster: <name=module:model, ...>
Seat: <name>
Stopped: <stop cause and outcome, or the stopping done's seq and outcome line>
Inputs: <present inputs; missing inputs and what they limit>
Findings: <count by category>

## Timeline

- <seq or time>: <step, one line>

## Findings

| # | Category | Owner | Finding |
| --- | --- | --- | --- |
| 1 | <category> | <owner> | <one-line summary> |

## Assessment

<how the run went overall: what worked, where time and launches went, and the costliest friction>
```

The Timeline is the run's steps, not every message: each `turn`, launch batch, finding, settle, fix, error, ask, and the `done`. List every finding. Do not truncate.

## Remediation Summary

Outcome values: `Fixed`, `Skipped`, `Ticket: <id>`, `Noted`, and `Pending` for a finding not processed when Save ran.

```
| # | Category | Finding | Outcome |
| --- | --- | --- | --- |
| 1 | prompt | <brief description> | Fixed |
```

## Ticket (T)

Before showing either prompt, run `command -v tk`. Offer `T` only if it succeeds.

1. `tk create --scope <scope>` with a title from the finding's short title. The scope follows the Owner table. Top-level `T` creates one ticket per owning scope for the remaining findings, then stops
2. `start get contexts:ticket/writing` if it is not already loaded
3. Fill under the H1 that `tk create` printed. Fold the instance, cause, cost, and recommended resolution into the profile that fits. Quote the evidence by `seq` and log line, without session ids, pids, tokens, or holders
4. `tk sync --scope <scope>` after the body fill. Do not push the scope with host git
5. Track as `Ticket: <id>`

## Save

Write the report only when asked, including by `S`, or when told to proceed without intervention. Use the path given. With no path when asked, ask. With no path when proceeding without intervention, write `.start/reviews/YYYY-MM-DD-conversation-review-NN.md`, with `NN` from `01` against existing files for that date. Include the Remediation Summary with current outcomes. Confirm the file written.

## Commands

### Top-level Prompt

Include the Ticket line only if the Ticket check succeeded.

```
- (C)ontinue: walk through the findings one at a time
- (A)ll: apply the recommended resolution to every finding
- (S)ave: write the report and stop
- (T)icket: create tk tickets for the remaining findings and stop
```

### Per-item Prompt

```
(R)ecommended  (N)ext  (T)icket  (S)ave
```

## Material
