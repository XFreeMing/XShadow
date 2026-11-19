// Regressions for the shared member-todo generator and its distributed copies.
import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { skillFiles } from "./agent-team-template.mjs";
import {
  buildMemberTodo, CHIEF_OF_STAFF_NAME, duplicateIdentifier, hasDecisionKey, sampleTodoInputs,
  sampleTodos, titleCandidates, validateTodoInput, validateMemberTodo,
} from "../agent-team/skills/rd-acceptance/scripts/member-todo.mjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const ACCEPTANCE_TOOL = join(ROOT, "agent-team/skills/rd-acceptance/scripts/member-todo.mjs");
const PM_TOOL = join(ROOT, "agent-team/skills/rd-project-management/scripts/member-todo.mjs");

function outsideRepo() {
  const dir = mkdtempSync(join(tmpdir(), "member-todo-"));
  return dir;
}

function runTool(tool, args, cwd) {
  return execFileSync(process.execPath, [tool, ...args], { cwd, encoding: "utf8", stdio: "pipe" });
}

function runToolResult(tool, args, cwd) {
  try {
    return { status: 0, stdout: runTool(tool, args, cwd), stderr: "" };
  } catch (error) {
    return {
      status: error.status,
      stdout: String(error.stdout ?? ""),
      stderr: String(error.stderr ?? ""),
    };
  }
}

test("the generator produces all four kinds and validates its own output", () => {
  const samples = sampleTodos();
  assert.deepEqual(samples.map(({ todo }) => todo.kind), ["material", "authorization", "choice", "decision"]);
  for (const { input, todo } of samples) {
    assert.deepEqual(validateTodoInput(input), []);
    assert.deepEqual(validateMemberTodo(todo), []);
    assert.ok(todo.first_screen.includes("**你需要做一件事：**"));
    assert.ok(todo.description.includes("## 技术附录（团队内部，无需阅读）"));
    assert.ok(todo.description.includes("multica-member-todo"));
  }
  const decision = samples[3].todo;
  assert.equal(decision.title, "「PDF 引用跳转」需要你做一个取舍（DEV-58）");
  assert.match(decision.first_screen, /点里面的引用打不开原文/);
  assert.match(decision.first_screen, /本期先发/);
  assert.match(decision.first_screen, /等引用能跳转后再发/);
  assert.match(decision.first_screen, /只回复不会自动接续/);
  assert.doesNotMatch(decision.first_screen, /acc-[0-9a-f]{16}|标准版本|候选|C1|DEV-58/);
});

test("the shipped skill file runs --sample and --check outside the repository", () => {
  const dir = outsideRepo();
  try {
    for (const tool of [ACCEPTANCE_TOOL, PM_TOOL]) {
      const sample = JSON.parse(runTool(tool, ["--sample"], dir));
      assert.equal(sample.samples.length, 4);
      const inputPath = join(dir, "input.json");
      writeFileSync(inputPath, JSON.stringify(sample.samples[0].input));
      assert.deepEqual(JSON.parse(runTool(tool, ["--check", "--input", inputPath], dir)), { ok: true });
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("the installed copies stay identical so one precheck serves both skills", () => {
  assert.equal(readFileSync(ACCEPTANCE_TOOL, "utf8"), readFileSync(PM_TOOL, "utf8"));
});

test("both skill bundles ship the runnable generator and the member format guide", () => {
  const acceptance = skillFiles(join(ROOT, "agent-team/skills/rd-acceptance")).map((file) => file.path);
  const pm = skillFiles(join(ROOT, "agent-team/skills/rd-project-management")).map((file) => file.path);
  assert.ok(acceptance.includes("scripts/member-todo.mjs"));
  assert.ok(pm.includes("scripts/member-todo.mjs"));
  assert.ok(pm.includes("references/member-todo.md"));
  const guide = readFileSync(join(ROOT, "agent-team/skills/rd-project-management/references/member-todo.md"), "utf8");
  for (const needle of ["scripts/member-todo.mjs --check", "成员待办自身", "同键直接复用", "（第 2 项）",
    "skill list --output json", "skill get \"$SKILL_ID\" --with-content", "只回复不会自动接续"]) {
    assert.ok(guide.includes(needle), `member format guide is missing ${needle}`);
  }
  const common = readFileSync(join(ROOT, "agent-team/common.md"), "utf8");
  assert.match(common, /已安装 `rd-project-management` 技能里的 `scripts\/member-todo\.mjs`/);
});

test("an invalid check exits 2 and names the concrete gap", () => {
  const dir = outsideRepo();
  try {
    const input = sampleTodoInputs()[3];
    input.options = [input.options[0]];
    const inputPath = join(dir, "bad.json");
    writeFileSync(inputPath, JSON.stringify(input));
    let failed = null;
    try {
      runTool(PM_TOOL, ["--check", "--input", inputPath], dir);
    } catch (error) {
      failed = error;
    }
    assert.equal(failed?.status, 2);
    assert.match(String(failed?.stdout), /至少两个真实可选项/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("the acceptance counterexamples fail both paths without writing a todo", () => {
  const choiceInput = sampleTodoInputs()[2];
  const materialInput = sampleTodoInputs()[0];
  const decisionInput = sampleTodoInputs()[3];
  const cases = [
    {
      name: "C2 missing reply example",
      input: {
        ...choiceInput,
        options: choiceInput.options.map((option, index) => {
          if (index > 0) return option;
          const { example: _example, ...rest } = option;
          return rest;
        }),
      },
      gap: /缺少回复示例 example/,
    },
    {
      name: "C3 engineering word in the title",
      input: { ...choiceInput, title: `${choiceInput.title}（验收回执）` },
      gap: /标题里有内部证据用语/,
    },
    {
      name: "C4 reply-only completion against a done wait",
      input: { ...materialInput, done: "只回复你的选择，不需要把本事项标记为完成。" },
      gap: /done 与 completion_signal=done 矛盾/,
    },
    {
      name: "C6 internal verification in an option example",
      input: {
        ...choiceInput,
        options: choiceInput.options.map((option, index) => (
          index === 0 ? { ...option, example: "先运行测试并附上日志，再回复金额" } : option
        )),
      },
      gap: /选项示例里有内部证据用语/,
    },
    {
      name: "C7 placeholder entry",
      input: { ...materialInput, where: "入口待确认，稍后发给你。" },
      gap: /入口或回复位置（where）.*占位信息/,
    },
    {
      name: "C6 internal verification as the completion action",
      input: { ...materialInput, done: "请运行测试并附上日志，然后把本事项标记为完成。" },
      gap: /done 里有内部证据用语/,
    },
    {
      name: "C3 engineering receipt in the continuation text",
      input: { ...decisionInput, next: "本事项完成后，原事项会自动继续处理，并把验收回执发给你。" },
      gap: /next 里有内部证据用语/,
    },
    {
      name: "C4 reply-triggered continuation against a done wait",
      input: { ...decisionInput, next: "回复后，原事项会自动继续处理。工作完成后会通知你。" },
      gap: /next 与 completion_signal=done 矛盾/,
    },
    {
      name: "C7 deferred entry with 稍后公布",
      input: { ...materialInput, where: "具体入口稍后公布。" },
      gap: /入口或回复位置（where）.*占位信息/,
    },
  ];
  for (const { name, input, gap } of cases) {
    const dir = outsideRepo();
    try {
      const inputPath = join(dir, "input.json");
      writeFileSync(inputPath, JSON.stringify(input));
      const check = runToolResult(PM_TOOL, ["--check", "--input", inputPath], dir);
      assert.equal(check.status, 2, `${name}: --check should exit 2 (${check.stdout}${check.stderr})`);
      assert.match(`${check.stdout}${check.stderr}`, gap, name);

      const outDir = join(dir, "out");
      const generate = runToolResult(PM_TOOL, ["--input", inputPath, "--out-dir", outDir], dir);
      assert.equal(generate.status, 2, `${name}: generation should exit 2 (${generate.stdout}${generate.stderr})`);
      assert.match(`${generate.stdout}${generate.stderr}`, gap, name);
      assert.equal(existsSync(outDir), false, `${name}: generation must not write todo files`);
      assert.equal(existsSync(join(dir, ".member-todo")), false, `${name}: no default output directory either`);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  }
});

test("title candidates are deterministic and stay within the title limit", () => {
  const base = "「PDF 引用跳转」需要你做一个取舍（DEV-7）";
  const first = titleCandidates(base);
  assert.equal(first[0], base);
  assert.equal(first[1], `${base}（第 2 项）`);
  assert.deepEqual(first, titleCandidates(base));
  for (const title of first) assert.ok([...title].length <= 60, title);
  const long = titleCandidates("这是一个很长的标题".repeat(10));
  assert.ok([...long[0]].length <= 60);
  assert.ok(long[1].endsWith("（第 2 项）"));
  assert.ok([...long[1]].length <= 60);
});

test("a duplicate title is the same request only when the body key matches", () => {
  const key = "acc-0123456789abcdef";
  const other = "acc-fedcba9876543210";
  const withAppendix = `正文\n---\n## 技术附录（团队内部，无需阅读）\n\ndecision_key: ${key}`;
  const withMarker = `正文\n<!-- multica-acceptance-decision {"decision_key":"${key}"} -->`;
  assert.equal(hasDecisionKey(withAppendix, key), true);
  assert.equal(hasDecisionKey(withMarker, key), true);
  assert.equal(hasDecisionKey(withAppendix, other), false);
  assert.equal(hasDecisionKey("正文\n<!-- multica-member-todo {} -->", key), false);
  assert.equal(
    duplicateIdentifier("Active duplicate issue exists: DEV-42 「PDF 引用跳转」需要你做一个取舍（status: todo). Set allow_duplicate=true"),
    "DEV-42",
  );
  assert.equal(duplicateIdentifier("some other error"), null);
});

test("a completion signal that the platform will not deliver is rejected", () => {
  const build = (overrides) => validateTodoInput({ ...sampleTodoInputs()[2], ...overrides });
  assert.ok(build({ next: "收到回复后，原事项会自动继续处理。" }).some((e) => /next/.test(e)));
  assert.ok(build({ completion_signal: "manager", next: "项目经理会查看回复。" }).some((e) => /next/.test(e)));
  assert.deepEqual(build({ next: "项目经理会在本待办收到你的回复，并接回原事项继续处理。" }), []);
  const done = sampleTodoInputs()[0];
  assert.ok(validateTodoInput({ ...done, done: "在本事项回复文件", next: "本事项完成后，原事项会自动继续处理。" }).some((e) => /done/.test(e)));
});

test("a done-wait generates its trigger from the signal and rejects reply-triggered wording", () => {
  const decisionInput = sampleTodoInputs()[3];
  const legal = {
    ...decisionInput,
    next: "回复后，请把本事项标记为完成，原事项会自动继续处理。",
  };
  const contradictory = [
    "你一回复，原事项就会自动继续处理。请在本事项标记为完成后等通知。",
    "本事项标记为完成只是可选操作，你一回复，原事项就会自动继续处理。",
    "原事项收到你的回复就会自动继续处理，之后可把本事项标记为完成。",
  ];
  const dir = outsideRepo();
  try {
    const legalPath = join(dir, "legal.json");
    writeFileSync(legalPath, JSON.stringify(legal));
    const legalCheck = runToolResult(PM_TOOL, ["--check", "--input", legalPath], dir);
    assert.equal(legalCheck.status, 0, `legal --check: ${legalCheck.stdout}${legalCheck.stderr}`);

    const legalOut = join(dir, "legal-out");
    const legalGenerate = runToolResult(PM_TOOL, ["--input", legalPath, "--out-dir", legalOut], dir);
    assert.equal(legalGenerate.status, 0, `legal generation: ${legalGenerate.stdout}${legalGenerate.stderr}`);
    const body = readFileSync(join(legalOut, "body.md"), "utf8");
    assert.match(body, /怎样算完成：.*标记为完成/);
    assert.match(body, /之后：回复后，请把本事项标记为完成/);

    for (const [index, next] of contradictory.entries()) {
      const inputPath = join(dir, `bad-${index}.json`);
      writeFileSync(inputPath, JSON.stringify({ ...decisionInput, next }));
      const check = runToolResult(PM_TOOL, ["--check", "--input", inputPath], dir);
      assert.equal(check.status, 2, `contradictory ${index} --check: ${check.stdout}${check.stderr}`);
      assert.match(`${check.stdout}${check.stderr}`, /next 与 completion_signal=done 矛盾/);

      const outDir = join(dir, `bad-out-${index}`);
      const generate = runToolResult(PM_TOOL, ["--input", inputPath, "--out-dir", outDir], dir);
      assert.equal(generate.status, 2, `contradictory ${index} generation: ${generate.stdout}${generate.stderr}`);
      assert.match(`${generate.stdout}${generate.stderr}`, /next 与 completion_signal=done 矛盾/);
      assert.equal(existsSync(outDir), false, `contradictory ${index} generation must not write todo files`);
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }

  const comment = sampleTodoInputs()[2];
  assert.ok(validateTodoInput({ ...comment, next: "请在本事项标记完成后等通知。" })
    .some((error) => /标记完成，但 completion_signal=comment/.test(error)));
  assert.ok(validateTodoInput({ ...comment, next: "项目经理收到你的回复后，原事项会自动继续处理。" })
    .some((error) => /自动继续/.test(error)));

  // A business-only tail gets the signal's fixed trigger sentence prepended.
  const withTail = buildMemberTodo({ ...decisionInput, next: "工作完成后我会通知你。" });
  assert.match(withTail.first_screen, /之后：先在本事项标记完成，原事项随后继续处理。工作完成后我会通知你。/);
});

test("internal evidence, placeholders and engineering markers stay out of the first screen", () => {
  const base = sampleTodoInputs()[2];
  for (const [field, value, needle] of [
    ["title", "订单列表先显示金额还是状态 acc-0123456789abcdef", "决策键"],
    ["problem", "请复跑脚本并附上日志，确认金额对得上。", "内部证据"],
    ["title", "订单列表先显示金额还是状态（待定）", "占位"],
    ["action", "在 <入口> 里选择。", "占位模板"],
  ]) {
    const errors = validateTodoInput({ ...base, [field]: value });
    assert.ok(errors.some((e) => e.includes(needle)), `${field}: ${errors.join(" | ")}`);
  }
  assert.ok(validateTodoInput({ ...base, options: [{ ...base.options[0], default: true }, base.options[1]] })
    .some((e) => /默认选择/.test(e)));
});

test("the generated todo never puts the decision key or a revision in the visible part", () => {
  const todo = buildMemberTodo(sampleTodoInputs()[3]);
  const visible = todo.description.split("\n---\n")[0];
  assert.doesNotMatch(visible, /acc-[0-9a-f]{16}|\b[0-9a-f]{7,40}\b|standard-v1|C1|候选/);
  assert.match(todo.description, /^decision_key: acc-0123456789abcdef$/m);
  assert.equal(todo.metadata.acceptance_decision_key, "acc-0123456789abcdef");
  assert.deepEqual(todo.metadata.member_todo_key ? todo.title_candidates[0] : null, todo.title);
});
test("the receiver is always the configured chief of staff", () => {
  const base = sampleTodoInputs()[2];
  assert.ok(validateTodoInput({ ...base, handler: "项目经理" })
    .some((error) => /接件人统一为幕僚长/.test(error)));
  assert.ok(validateTodoInput({ ...base, handler: { name: "研发经理" } })
    .some((error) => /接件人统一为幕僚长/.test(error)));
  assert.deepEqual(validateTodoInput({ ...base, handler: CHIEF_OF_STAFF_NAME }), []);
  assert.deepEqual(validateTodoInput(base), []);
});

test("the marker carries the readable question, fixed receiver and atomic options", () => {
  const input = sampleTodoInputs()[2];
  const todo = buildMemberTodo(input);
  const marker = JSON.parse(todo.description.match(/<!-- multica-member-todo (.*?) -->/)[1]);
  assert.equal(marker.handler, CHIEF_OF_STAFF_NAME);
  assert.equal(marker.todo_key, todo.metadata.member_todo_key);
  assert.equal(marker.problem, input.problem);
  assert.equal(marker.why_you, input.why_you);
  assert.equal(marker.action, input.action);
  assert.ok(marker.options.length >= 2);
  for (const option of marker.options) {
    assert.ok(option.key && option.label && option.consequence);
  }
  // An arbitrary handler is rejected before any marker is generated.
  assert.throws(
    () => buildMemberTodo({ ...input, handler: "项目经理" }),
    /接件人统一为幕僚长/,
  );
});

test("the generator, the team template and the client agree on the receiver", () => {
  const team = JSON.parse(readFileSync(join(ROOT, "agent-team/team.json"), "utf8"));
  const chief = team.agents.find((agent) => agent.name === CHIEF_OF_STAFF_NAME);
  assert.ok(chief, "team.json declares the configured receiver");
  assert.equal(chief.key, "chief");
  assert.match(chief.instructions, /multica-member-answer/);
  const skill = readFileSync(join(ROOT, "agent-team/skills/rd-coordinate/SKILL.md"), "utf8");
  assert.match(skill, /multica-member-answer/);
  const client = readFileSync(join(ROOT, "packages/core/issues/member-todo.ts"), "utf8");
  assert.match(client, /MEMBER_TODO_HANDLER = "幕僚长"/);
});
