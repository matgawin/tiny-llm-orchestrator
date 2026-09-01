# Use cases beyond documentation

STE was built for aircraft maintenance manuals. The same properties apply to
technical text where misreading has a cost: one meaning per word, short
sentences, and condition-first commands.

Each case below names the mode and the adaptations.

## Error messages and CLI output

Mode: procedural. An error message tells a reader what failed and what to do
next.

Pattern: state what happened in simple past. State the cause if known. Give the
command or condition to fix it.

> **Before:** Oops! Something went wrong while attempting to establish a connection. Please ensure your credentials are properly configured and try again.
> **After:** Connection to the database failed. The password for user `app` was not correct. Set `DB_PASSWORD` and connect again.

## Runbooks and standard operating procedures

Mode: strict-leaning procedural. An on-call runbook must be easy to read under
time pressure.

- Every step imperative, one instruction per step, conditions first.
- Warnings before the step, command first, risk second.
- Enforce the 20-word limit.

## Incident reports and postmortems

Mode: descriptive. Use simple past for timelines. Present perfect can hide
when events happened.

> **Before:** We have identified an issue that may have impacted some users' ability to access the service.
> **After:** Between 14:02 and 14:31 UTC, 12% of requests failed. A deploy at 14:00 removed the cache warmup step.

State what is known. Use `unknown` for facts that the report does not prove.

## Commit messages and PR descriptions

Mode: descriptive body, imperative subject. Use an imperative subject line.
Put plain past facts in the body. Delete "this PR aims to".

## API changelogs and release notes

Mode: descriptive. Use one entry for one change. Use one sentence where
possible.

`Breaking:` entries follow the warning pattern. Put the command first:
"Update your calls to `v2/users`. The `name` field split into `first_name` and
`last_name`."

## Instructions for AI agents (prompts, AGENTS.md, skills)

Mode: procedural. A system prompt is a procedure for a reader with no chance to
ask follow-up questions.

- One instruction per sentence keeps rules independently quotable and hard to half-follow.
- One word, one meaning prevents the model from treating "check", "verify", and "validate" as three different operations.
- Condition-first ("If the build fails, stop") beats trailing conditions, which models drop.
- Do not use "should" for a requirement. Write "must" or delete the rule.

## Support macros and status-page updates

Mode: descriptive, 25-word limit. State the user-visible effect and the next
system action.

> **Before:** We sincerely apologize for any inconvenience this may have caused.
> **After:** The API was down for 18 minutes. Uploads made during this time were saved and will process today.

## Translation and localization prep

Mode: strict. Use one meaning per word and complete grammar before translation.
Keep articles and the conjunction "that" when they remove ambiguity.

## UI copy and empty states

Mode: procedural, hard length limits. Buttons and labels are technical names.
Body copy follows the rules.

> **Example:** No projects yet. Create a project to start.

## Where STE does not fit

Marketing pages, launch posts, blog voice, and brand writing need a different
standard. Use STE for the technical docs that those pages link to.
