import { test } from "node:test";
import assert from "node:assert/strict";
import {
  splitPipelineCommand,
  pipelineLineDiff,
} from "../features/pipelines/PipelineEditor";
test("pipeline command arguments preserve quoted spaces and escaped quotes", () => {
  assert.deepEqual(
    splitPipelineCommand(
      String.raw`python -c "print(\"hello world\")" --name 'training run'`,
    ),
    ["python", "-c", 'print("hello world")', "--name", "training run"],
  );
  assert.deepEqual(splitPipelineCommand('python train.py --empty ""'), [
    "python",
    "train.py",
    "--empty",
    "",
  ]);
  assert.throws(
    () => splitPipelineCommand('python -c "unclosed'),
    /Close the quoted/,
  );
});
test("revision comparison retains unchanged context and distinguishes replaced lines", () => {
  assert.deepEqual(
    pipelineLineDiff("name: flow\nversion: 1", "name: flow\nversion: 2"),
    [
      { type: "same", text: "name: flow" },
      { type: "del", text: "version: 1" },
      { type: "add", text: "version: 2" },
    ],
  );
});
