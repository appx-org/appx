---
name: arch-doc
description: Generates comprehensive architecture documentation for code changes, PRs, and implementation phases. Use this skill whenever the user asks to document code changes, write an architecture doc, create a technical reference, or help a human reviewer understand what a coding agent built. Trigger on phrases like "arch doc", "architecture doc", "document this PR", "write arch doc for phase X", "generate a living reference", "document these changes", "/arch-doc". This is especially valuable after a coding agent completes a feature or phase — the document helps a human reviewer understand, audit, and manually verify the work before merging.
---

# Architecture Documentation

You are generating a **living reference document** — not a changelog, not a commit message, not a list of diffs. The goal is to give a human reviewer full context to understand, audit, and manually test the code changes without needing to read every file themselves. Tell a coherent story. Explain *why*, not just *what*.

---

## Step 1 — Determine Scope

Identify what to document. The user may have given you a scope; if not, default to "current branch vs main".

| User says | How to find the changes |
|---|---|
| Phase or feature name (e.g. "phase 2") | `git log --oneline -30` to find relevant commits; `git diff <base>...HEAD --name-only` |
| Branch name | `git diff main...<branch> --name-only` and `git log main...<branch> --oneline` |
| PR number | `gh pr view <n>` and `gh pr diff <n>` (if `gh` CLI available) |
| "Current changes" / nothing | `git diff main...HEAD --name-only` and `git log main...HEAD --oneline` |

Always run `git log --oneline -20` regardless, to understand the commit narrative.

---

## Step 2 — Read Everything Before Writing Anything

Read **all changed files completely**. For each file, ask:
- What is this file's role in the system?
- What specifically changed, and why?
- What would a reviewer need to know that isn't obvious from reading the diff?
- Are there any non-obvious decisions, trade-offs, or risks here?

Also read adjacent unchanged files when they provide context — if a store changed, read its tests and the handler that calls it.

Do not start drafting until you have read everything. The document's coherence depends on understanding the full picture first.

---

## Step 3 — Write the Document

Save to `docs/architecture/` with a descriptive filename:
- Phase-based: `arch_phase_3.md`
- Feature-based: `arch_auth_refresh.md`
- PR-based: `arch_pr_42.md`

Include all eight sections below, in order.

---

### Section 1 — Table of Contents

Anchor links to every major section and sub-section. Essential for long documents. Place at the top.

---

### Section 2 — Overview

A narrative briefing — the 5-minute version a tech lead would give before a code review. Cover:

- **The problem being solved** — why was this work needed?
- **The key design decisions** — what approaches were chosen, and why?
- **How the pieces fit together** — the conceptual model, not just the file list
- **Significant trade-offs or alternatives considered** — what was *not* done, and why?

A reader finishing this section should understand the design philosophy before touching any code. If there are multiple interacting mechanisms (e.g. async operations + status machine + polling), explain how they combine to solve a problem — that's the story.

---

### Section 3 — System Map

ASCII diagram(s) with new and changed components labelled `[NEW]` or `[UPDATED]`. Show:

- Component relationships and data flow
- New or changed API endpoints (list in a table: method, path, auth, request, response, errors)
- Database schema additions/changes
- New external dependencies
- State machines (if applicable) — state diagrams clarify state machine logic better than prose

Use tables for structured data (API endpoints, DB fields, error codes, status transitions).

**The reader should be able to identify at a glance what Phase N added versus what was already there.**

---

### Section 4 — Code Review Guide

The most important section for the human reviewer. Walk through the changed files in **logical dependency order** (data models → stores → business logic → HTTP handlers → frontend). A reviewer reading top-to-bottom should never encounter a reference to something they haven't seen yet.

For each file or logical group:

**What changed and why** — the motivation behind the change, not a summary of the diff.

**Key decisions** — explain the non-obvious choices. Why this structure and not that one? Why synchronous here but async there?

**What to verify** — specific questions the reviewer should ask themselves:
- "Is the concurrency safe here?"
- "Could this race condition occur if two requests arrive simultaneously?"
- "Is the error mapping correct?"
- "Are there edge cases in this validation that the tests don't cover?"

Actively highlight:
- Concurrency patterns — are they safe? Is there a simpler alternative?
- Security-sensitive code — auth, secrets, user input validation
- Fragile assumptions — e.g. string-matching on error messages, driver-specific behaviour
- Code that looks incomplete or has obvious alternatives worth discussing
- Forward-looking scaffolding that exists but isn't wired up yet

---

### Section 5 — Testing Guide

**Automated test coverage** — for each test file:
- What scenarios it covers
- What the key setup helpers do (so the reviewer can orient quickly)
- Gaps in coverage worth noting

**Manual verification checklist** — an ordered, actionable sequence:

```
[ ] Step 1: exact action → what to observe
[ ] Step 2: exact action → what to observe
```

Include: happy paths, error cases, edge cases, anything async (requires waiting/polling), crash recovery scenarios, UI state transitions. Order the checklist so later steps build on earlier ones.

---

### Section 6 — Architecture and Code Pitfalls

Issues that **exist in the current implementation**. This is a gift to the next developer, not a criticism. For each:

- **Location** — file name and function/line if known
- **The problem** — what is wrong or risky
- **Why it matters** — impact if triggered (severity: low / medium / high)
- **What a fix looks like** — enough to act on

Check systematically: race conditions, error handling gaps, security concerns, brittle assumptions (string matching, driver-specific formats), missing validation, performance bottlenecks, code that silently succeeds on failure.

---

### Section 7 — Fixed Pitfalls

Issues **identified during this implementation and corrected before merge**. This section documents the decision trail so future readers understand why things look the way they do.

Format:
> **Problem:** what was wrong  
> **Fix:** what was done and why

This is especially valuable for non-obvious fixes — where the code might look strange without knowing what it replaced.

---

### Section 8 — TODOs and Future Improvements

- Explicit `TODO` / `FIXME` comments found in code (with `file:line` references)
- Known limitations accepted as deliberate trade-offs, and what it would take to lift them
- Work deferred to a future phase, and why it was deferred
- Prerequisites for the next phase of development

---

## Writing Style

**Tell a story.** The Overview should read like a tech lead briefing, not a git log. Use prose to explain the *why*; use tables and diagrams to present the *what*.

**Explain reasoning, not just decisions.** "Transitions are enforced at the database level" is fine. "Transitions are enforced at the database level *because two concurrent requests could both observe `stopped` before either writes `starting`*" is useful.

**Be specific about pitfalls.** "Be careful with concurrency" is unhelpful. "These two code paths both call `SetRunning` unconditionally — if they run on the same project simultaneously, the second write silently wins" is actionable.

**Calibrate depth to complexity.** A 3-line helper doesn't need 3 paragraphs. A 200-line async lifecycle manager deserves a sub-walkthrough with its own mini-diagram.

**Use diagrams when they save words.** A state machine diagram is worth more than three paragraphs. ASCII art is fine; clarity matters more than beauty.

---

## After Writing

If `CLAUDE.md` has a Documentation section that references architecture files, add an entry pointing to the new document.

Do not add emojis unless the project already uses them in documentation.
