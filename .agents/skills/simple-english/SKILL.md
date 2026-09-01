---
name: simple-english
description: |
  Write, rewrite, or check repository text with ASD-STE100 Simplified Technical
  English principles. Use for documentation, READMEs, runbooks, procedures,
  error messages, release notes, incident reports, API guides, AGENTS.md files,
  skills, and text that must translate well. Also use when the user says
  "STE", "Simplified Technical English", "ASD-STE100", "de-slop", "make this
  readable", or "write for non-native readers".
---

# simple-english

Use this skill to make technical text clear, exact, and easy to translate.

Do not assign authorship scores to text. Do not make text sound casual,
personal, or natural for its own sake. For this repo, the standard is plain,
source-backed technical English.

## Required References

Read these files when they apply:

- `references/checklist.md` before final output for any rewrite or durable doc
  edit.
- `references/rules.md` when the user asks for STE, compliance, rule numbers,
  or a violation report.
- `references/use-cases.md` when the target is not a normal documentation page.

## Modes

Use plain repo English by default.

Use strict STE when the user asks for STE, ASD-STE100, compliance, translation
prep, or a rule-numbered report. In strict mode, tell the user that full
compliance needs the official ASD-STE100 dictionary.

## Classify The Text

Classify each passage before you edit it:

- Procedural text tells the reader what to do.
- Descriptive text explains what a thing is or does.

Procedural text uses imperative verbs. Use a 20-word sentence limit.

Descriptive text uses simple present, simple past, or simple future. Use a
25-word sentence limit.

Do not mix procedural and descriptive text in one paragraph. A note in a
procedure is descriptive.

## Core Rules

Apply these rules in all modes:

1. Use one name for one thing.
2. Use one verb for one action type.
3. Use active voice unless the actor is unknown.
4. Use simple tenses.
5. Do not use contractions.
6. Do not use semicolons.
7. Put a required condition before the command.
8. Use one instruction per sentence.
9. Break noun chains longer than three words.
10. Keep code, commands, identifiers, quoted errors, and proper nouns unchanged.

## Repo Prose Audit

For repository docs and skills, also remove these patterns unless they are
untouchables:

- vague claims without a route, command, path, error, number, example, or
  explicit condition
- filler words such as `delve`, `leverage`, `utilize`, `robust`,
  `comprehensive`, `streamline`, `facilitate`, `significant`, and `notable`
- reflexive hedges such as `generally`, `typically`, `often`, `it is important
  to note`, and `it is worth noting`
- stock transitions such as `Furthermore`, `Moreover`, `Additionally`,
  `This highlights`, and `As previously mentioned`
- marketing or status-update register in engineering docs
- repeated summaries that restate the previous paragraph
- mid-sentence colon patterns such as `The problem: X`.

If a flagged word is an untouchable, keep it.

## Check Mode

When the user asks you to check text, report violations in this format:

- rule or audit item
- offending text
- compliant rewrite

Use rule numbers only when you read `references/rules.md` and the cited rule
appears there.

For strict STE compliance reports, end with:

`No tool can guarantee ASD-STE100 compliance. Final approval rests with the writer. The official standard is a free download at asd-ste100.org.`

## Untouchables

Do not change:

- code blocks
- inline code
- commands
- file paths
- API names
- SQL names
- migration names
- role names
- quoted output
- quoted error messages
- proper nouns.
