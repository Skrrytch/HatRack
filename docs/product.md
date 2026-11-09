# HatRack: product definition

This page says what HatRack is for, what it must not become and how we tell
that it works. Every ticket and every design note is measured against it.
When a plan and this page disagree, change one of them on purpose.

## 1. Vision

> **HatRack records what you worked on, in blocks you can trust, so that
> recording your working time takes minutes and not guesswork.**

During the day you keep putting on hats: you work in a context, a role or a
project, and switch to the next one. Inside a hat you do different
activities, and each activity touches concrete things: tickets, branches,
concepts. HatRack notices this as it happens, writes it down, groups it into
sensible blocks and explains every block. You define the frame once and
correct it now and then. The more you do, the less HatRack needs you.

## 2. The problem

- Time sheets ask "what did you do from 10 to 11?" Hours later nobody knows,
  least of all when two tickets ran side by side and a mail came in between.
- Booking by hand interrupts the work. Not booking makes the day a guess.
- Tools that log every window give a heap of fragments, not a day you can
  read or book.
- Switching context is also a mental act. Seeing which hat you wear helps you
  stay in it, and tells others (a lamp at home) what you are doing.

## 3. Who it is for

- **Primary:** developers and DevOps people on Linux who switch between
  projects, roles or customers and have to record their time, often by ticket.
- **Also:** anyone with a few contexts (project, company matters, private)
  who wants to know where the day went, without a fine grain.
- The number of hats varies: some people have two or three, others many. Both
  must work without extra effort for the other case.

## 4. The model: hat, activity, detail

| Level | What it is | Example | Who defines it |
|---|---|---|---|
| **Hat** | the context: a role, a project, a customer, private | VENOM, Company, Private | the user, up front |
| **Activity** | the kind of work inside a hat; a finite list | implementation, story concept, testing on integration, customer support | the user, up front, per hat |
| **Detail** | what exactly: ticket key, branch, concept, topic | `ACME-101`, `ACME-101, ACME-117`, "training preparation" | found by HatRack or typed |

- **One hat at a time.** Hats are the rough order of the day.
- **Parallel work is visible.** Two tickets of the same activity in
  alternation are one block with both keys as its detail; different
  activities or hats are split by their observed time. HatRack never
  invents a split that only the user can know.
- **Blocks, not fragments.** A day reads as a few blocks per hour at most,
  each with hat, activity, detail and a reason.

## 5. Goals, in order of priority

1. **A reliable day afterwards.** At the end of the day (or the next morning)
   the log holds blocks that match what happened: right hat, right activity,
   details that point to the tickets. Reliability afterwards beats accuracy
   live.
2. **Hardly any interruption.** HatRack asks only when a decision cannot be
   made without the user, and then at a good moment, prefilled, answered in
   seconds. Otherwise it signals quietly that it needs help ("calls for
   help"): a mark in the tray, an entry in the day view, a hint that does not
   disturb. Only what is urgent may interrupt (see 7a).
3. **Explainable.** Every proposal says why: which rule, which ticket, what
   was learned, which window. A user can always see and correct it.
4. **Increasingly independent.** HatRack switches hats and names activities
   by itself where the evidence is clear. It learns from every booking and
   correction. Live switches may come late; the record gets fixed afterwards.
5. **The hat is visible.** Desktop look (wallpaper, colours, theme), tray,
   Home Assistant and other outputs show the worn hat. This supports focus and
   tells others; it serves the record, not the other way round.
6. **Easy to evaluate.** The user, or an AI through MCP, reads the day as
   blocks with sums per hat, activity and detail, and can book or export it.

## 6. How it works, in one paragraph

Sensors observe (active window, absence, later microphone and camera,
working directory and branch). Observations are evidence, kept for a limited
time, never the record. A classifier turns them into proposals using the
user's rules, known ticket projects and what it learned from earlier
bookings. Proposals are condensed into blocks per hour; a task stays open
until something ends it (absence, another hat, a while unseen). The log is
written by the user's confirmation, by rules the user allowed to act
automatically, or by a correction afterwards. Everything is shown in the web
UI and through MCP.

## 7. Principles

- **Do not guess.** No block without evidence. Unknown stays unknown and is
  shown as such; a weak clue is a proposal, never an entry.
- **Evidence is not the decision.** Observations never change the log by
  themselves; only the user, an allowed rule or a confirmed proposal does.
- **The end matters as much as the start.** A task ends by absence, a
  boundary set by the user, another hat, or not being seen for a while, not
  by a glance at another window.
- **Late is fine.** A visible switch may wait minutes; the record is fixed
  afterwards. No live hecticness.
- **Not too fine.** Short interruptions belong to the block they interrupt;
  a minimum block length keeps the day readable.
- **The user teaches, HatRack learns.** Sensors, tool settings (toolbox),
  rules and ticket systems are set up by the user; every booking trains.
- **Privacy by default, permission by choice.** HatRack may read what helps
  its purpose and what the system allows, but only what the user has
  granted. Sensors are off until switched on per device. Everything beyond
  the basics (app, window title, absence) is a **grant** on the page
  Freigaben: off by default, explained in plain words (what is read, why it
  helps, what is stored, for how long, who can see it), revocable at any
  time. Read data is used locally and stored as sparingly as the function
  allows (a rule's result rather than the raw text). Nothing leaves the
  user's own server, nothing is fetched from outside.
- **No dependency on one distribution, desktop or display server.** Desktop
  specifics live in backends.

## 7a. How HatRack gets attention

Three levels, from quiet to loud. Every message says why it comes, offers
the action that settles it, and never comes twice for the same thing. Each
kind can be switched off; a quiet time (meetings, outside working hours,
"do not disturb" of the desktop) holds back everything but urgent ones.

| Level | Form | When | Examples |
|---|---|---|---|
| **Signal** | mark on the tray icon, entry in "Needs you" in the day view | always, never disturbs | open hours, a proposal waiting, a sensor that stopped |
| **Hint** | desktop notification with low urgency: no sound, disappears by itself, bundled, preferably at a natural pause (back from absence, after a switch) | helpful, but can wait; on by default, switchable per kind | "VENOM · Entwicklung for 10 min, switch?", "switched to Meeting, undo?", "2 open hours", the end-of-day summary |
| **Urgent** | notification with normal urgency that stays until handled | only when waiting makes the record wrong or costly; on by default, switchable per kind | the clock runs although nobody is there or work ended long ago, the agent has not reached the server for hours, a grant stopped working |

## 8. What HatRack does not do

- **No surveillance.** It is the user's own tool. No team view of other
  people, no reports to an employer, no productivity scores or rankings.
- **No reading without a grant.** Without a grant HatRack sees app names,
  window titles and absence, nothing else. Whatever goes further (text of
  notifications, branch and folder names, subjects, microphone and camera
  state, files of other tools) needs its own grant (see 7). Read data is
  treated as data, never as instructions.
- **No recording of keystrokes, sound or screen images, ever.** No grant
  unlocks it. The purpose is assigning time, not reconstructing what was
  typed, said or shown. The page Freigaben states this limit first.
- **No cloud.** No service of ours, no login to ticket systems, no fetching
  of ticket data. Links to tickets are built from known patterns.
- **No guessing for completeness.** It does not fill a day just to make it
  look full.
- **Not a project management or ticket tool.** It refers to tickets; it does
  not manage them.
- **Not an invoicing tool.** It produces reliable blocks and sums; exports to
  time sheets or invoices are adapters on top, not the core.
- **No live obligation.** Being right later is the goal; the current display
  may lag.
- **No constant questions.** A question that could wait or be answered by
  evidence is not asked.

## 9. How we tell it works (targets, to be confirmed with real days)

| Measure | Target |
|---|---|
| Time to a bookable day | under 5 minutes of review per day |
| Blocks per working hour | about 2, rarely more than 4 |
| Active time with a hat after review | 100 %; before review, by evidence alone, at least 80 % once set up |
| Questions | at most a few per day, none for interruptions under 5 minutes |
| Explanation | every proposal and every automatic switch has a reason the user can read |
| Corrections | a corrected or accepted proposal never comes back |
| Privacy | nothing stored beyond what a switched-on sensor or a granted permission documents, and the user's own entries; every grant is off until the user turns it on |
| Notifications | at most a few hints per day; urgent ones only when the record would otherwise be wrong |

## 10. Stages, seen from the goals

| Stage | Goal it serves |
|---|---|
| MVP 1 to 6 (done) | hats, activities, details, log, look, server, web UI, MCP, absence: the frame and the record by hand |
| MVP 7 (done) | observation as evidence, privacy settings |
| MVP 8 | assignment: rules, tickets, learned clues, toolbox, page Freigaben (grants), working directory and branch |
| MVP 9 | the reliable day: condensed blocks, end of tasks, review, live switching, calls for help, MCP, day report |
| later | exports to time sheets, more sensors (meetings), Wayland, standalone mode |

## 11. Open questions

- Is "about 2 blocks per hour" right for people with many short support
  tasks, or do they need a finer setting?
- Which call for help works best without interrupting: tray mark, a daily
  digest, or a prompt at the next natural pause?
- How much setup is acceptable before the first useful day: a few rules, a
  ticket project, one toolbox page?
