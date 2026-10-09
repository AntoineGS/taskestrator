// tsk OpenCode plugin: when an agent claims a Beads task from a shell
// command, `tsk link` links the task to the agent's session so bt can show
// its live state. Install by symlinking this file into
// ~/.config/opencode/plugins/. It never fails or delays a tool call.

import { spawn } from "node:child_process"

// A cheap pre-filter; tsk link does the real parsing.
export function looksLikeClaim(command) {
  return (
    typeof command === "string" &&
    /\bbd\b/.test(command) &&
    /\bupdate\b/.test(command) &&
    /--claim|in_progress/.test(command)
  )
}

// The tsk arguments for a finished tool call, or undefined to do nothing.
export function linkArgs(event) {
  if (event?.tool !== "shell" || event.status !== "completed") return undefined
  if (typeof event.sessionID !== "string" || event.sessionID === "") return undefined
  const command = event.input?.command
  if (!looksLikeClaim(command)) return undefined
  const args = ["link", "--session", event.sessionID]
  const workdir = event.input?.workdir
  if (typeof workdir === "string" && workdir !== "") args.push("--workdir", workdir)
  args.push("--command", command)
  return args
}

export function start(args, run = spawn) {
  try {
    const child = run("tsk", args, { detached: true, stdio: "ignore" })
    child.on("error", () => {})
    child.unref()
  } catch {
    // tsk missing or not startable: linking is best effort.
  }
}

export default {
  id: "tsk.link",
  async setup(ctx) {
    await ctx.tool.hook("execute.after", (event) => {
      try {
        const args = linkArgs(event)
        if (args) start(args)
      } catch {
        // Never let linking affect the tool call.
      }
    })
  },
}
