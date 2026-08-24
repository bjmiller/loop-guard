---
name: avoid-loops
description: Use when working on tasks that involve running commands, tests, or builds repeatedly - teaches strategies to recognize and escape retry loops before a loop-guard breaker trips, including when to stop and ask the human instead of trying again.
---

# Avoiding loops and unproductive retries

You have a limited budget for repeating yourself. This skill keeps you inside it.

## The rules

1. **Cap blind retries at 2.** If the same command fails twice with the same
   error, a third identical attempt will produce the same error. Do not run it.

2. **Change something material before retrying:**
   - different inputs (flags, paths, versions)
   - different tool (read the file instead of grep-ping it; run one test instead of the suite)
   - different plan (fix the cause instead of treating the symptom)

3. **Read before you re-run.** After a failure, state what you expected vs.
   what happened in one sentence. If you cannot, you are guessing — go gather
   information instead.

4. **Escalate instead of looping.** If two materially different approaches both
   failed, stop and tell the human:
   - what you tried (both approaches, concisely)
   - what happened each time
   - your best hypothesis for why it keeps failing
   - what you would try next if you continued

5. **Never restate a near-identical response.** If you notice you just wrote
   almost the same thing again, you are stuck. Stop output; reassess.

## Recognizing you are in a loop

Warning signs:
- You are about to run a command you have already run with identical arguments.
- Your explanation for the failure is the same as last time.
- You keep saying "let me try again" without naming what changed.

If a harness injects a Loop-Guard message into your context: it has measured
that you are repeating yourself. Follow its required actions immediately — do
not apologize and continue the same approach.

## Why this matters

A monitoring layer may be watching this session. Repeating an identical call
three times trips an intervention; ignoring two interventions ends the session
and hands off to a fresh thread. Changing strategy early is always cheaper.
