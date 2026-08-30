# Subagents

Running the agent again, as a tool of its own: work that would fill this
run's context — reading a long file, sweeping a cluster, trying three things
to see which holds — is handed to a run that has a context of its own, and
comes back as one answer.

The family lives in `core/subagent` (not `core/tools`) for the same reason
the schedule family lives in `core/schedule`: the tools need the agent
itself, and the agent depends on the tools.

## Registration

```go
import "github.com/ishi-o/golem/core/subagent"

subagent.Register(provider, agent, cfg, nil, logger)
```

One call, during application setup: it registers the three tools and teaches
the agent to forget a run's subagents when the run ends. Registering the
family is the gate — a deployment that does not call `Register` never offers
the tools.

## The tools

| Tool | What it does |
| --- | --- |
| `StartSubagent` | Fires another run of the agent with its own context window and returns at once with an id. |
| `WaitForSubagent` | Blocks until the subagent finishes (or a poll interval passes) and reads its answer. |
| `CancelSubagent` | Stops a subagent whose answer is no longer wanted. |

Starting does not wait, so several subagents can be in the air at once and
the model decides when it wants each answer. What it cannot do is walk away:
the agent holds a run open until the subagents it started have finished, so
an answer nobody collects is still paid for and still reported. The tool
descriptions say so.

## What a subagent is

A subagent run is an ordinary run with a particular shape:

- **No conversation.** `SubagentScenario` attaches no memory, and the
  subagent gets a conversation id of its own, so it lands in no store
  whatever the backend does.
- **The brief is the whole task.** The subagent sees nothing of the
  conversation that started it — not the user message, not what the parent
  found, not the files it has open. Everything it needs goes in the prompt.
  `cfg.AI.SubagentPrompt` (a template over `{taskText}`) frames the brief;
  a blown-up template falls back to the brief as-is rather than losing the
  task.
- **Background.** No card, no stop button, no output of its own interleaved
  with the parent's conversation — and no ask: there is no surface to put a
  question on.
- **One level deep.** The scenario withholds the subagent tools and the
  schedule tools, enforced by name — a depth cap with no counter to get
  wrong. Unattended work must not leave work behind.
- **Same workspace.** It writes to the same workspace the parent does, so a
  file it leaves behind is a file the parent can read.

## What the parent sees

Only the parent's listeners hear about a subagent, through `OnSubagent`
events: started (with the one-line description), content-so-far while it
talks, usage as it spends, and the outcome with the final answer once it
ends. Content is never forwarded as `OnContent` — a surface renders that as
the reply, and the subagent's words are not the parent's reply. A
subagent's tokens count on the parent's turn: usage is forwarded to the
parent's `OnUsage` as well as attributed in the event.

Cancelling the parent cancels its subagents, down the tree; the parent's
own finish waits out its children first (bounded by
`cfg.AI.Tools.Subagent.WaitTimeout`, 10 minutes by default — within the
agent's shutdown drain window), so a straggler cannot outlive the turn by
much and cannot hang it for good. `WaitForSubagent` gives the turn back to
the model every `WaitPoll` (60s by default) rather than blocking forever.

## Configuration

```go
cfg.AI.SubagentPrompt                    // template over {taskText}
cfg.AI.Tools.Subagent.MaxConcurrent      // per parent run; default 10
cfg.AI.Tools.Subagent.WaitPoll           // one wait's bound; default 60s
cfg.AI.Tools.Subagent.WaitTimeout        // subagent age ceiling; default 10m
```
