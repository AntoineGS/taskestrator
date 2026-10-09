import test from "node:test"
import assert from "node:assert/strict"
import plugin, { looksLikeClaim, linkArgs, start } from "./tsk-link.js"

const shell = (command, extra = {}) => ({
  tool: "shell",
  status: "completed",
  sessionID: "ses_a",
  input: { command, ...extra },
})

test("looksLikeClaim", () => {
  assert.equal(looksLikeClaim("bd update tsk-a --claim"), true)
  assert.equal(looksLikeClaim("bd update tsk-a -s in_progress"), true)
  assert.equal(looksLikeClaim("bd show tsk-a"), false)
  assert.equal(looksLikeClaim("bd update tsk-a --notes x"), false)
  assert.equal(looksLikeClaim("go test ./..."), false)
  assert.equal(looksLikeClaim(undefined), false)
})

test("linkArgs for a claim", () => {
  assert.deepEqual(linkArgs(shell("bd update tsk-a --claim")), [
    "link", "--session", "ses_a", "--command", "bd update tsk-a --claim",
  ])
  assert.deepEqual(linkArgs(shell("bd update tsk-a --claim", { workdir: "sub" })), [
    "link", "--session", "ses_a", "--workdir", "sub", "--command", "bd update tsk-a --claim",
  ])
})

test("linkArgs ignores other calls", () => {
  assert.equal(linkArgs({ ...shell("bd update tsk-a --claim"), tool: "read" }), undefined)
  assert.equal(linkArgs({ ...shell("bd update tsk-a --claim"), status: "error" }), undefined)
  assert.equal(linkArgs(shell("ls")), undefined)
  assert.equal(linkArgs({ ...shell("bd update tsk-a --claim"), sessionID: undefined }), undefined)
  assert.equal(linkArgs(undefined), undefined)
})

test("start spawns tsk detached and never throws", () => {
  const calls = []
  const child = { on: (name) => calls.push(["on", name]), unref: () => calls.push(["unref"]) }
  start(["link"], (cmd, args, opts) => {
    calls.push([cmd, args, opts])
    return child
  })
  assert.deepEqual(calls[0], ["tsk", ["link"], { detached: true, stdio: "ignore" }])
  assert.deepEqual(calls.slice(1), [["on", "error"], ["unref"]])
  assert.doesNotThrow(() => start(["link"], () => { throw new Error("ENOENT") }))
})

test("setup registers execute.after and ignores non-claims", async () => {
  const hooks = []
  await plugin.setup({ tool: { hook: async (name, cb) => hooks.push([name, cb]) } })
  assert.equal(plugin.id, "tsk.link")
  assert.equal(hooks.length, 1)
  assert.equal(hooks[0][0], "execute.after")
  assert.doesNotThrow(() => hooks[0][1](shell("ls")))
  assert.doesNotThrow(() => hooks[0][1](null))
})
