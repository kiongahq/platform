import "./setup-dom";
import assert from "node:assert/strict";
import { afterEach, test } from "node:test";
import React from "react";
import { render, fireEvent, waitFor, cleanup } from "@testing-library/react";
import { ModelPredictForm, PREDICT_PLACEHOLDER } from "../features/operations/Resources";

Object.assign(globalThis, { FormData: window.FormData, self: window });
afterEach(cleanup);

function api() {
  const calls: Array<{ url: string; method: string; body: string | undefined }> = [];
  globalThis.fetch = async (input, init) => {
    calls.push({ url: String(input), method: init?.method || "GET", body: init?.body ? String(init.body) : undefined });
    return { ok: true, status: 200, text: async () => JSON.stringify({ predictions: [1] }) } as Response;
  };
  return calls;
}

test("payload starts empty with a labelled example in the placeholder, not a value", () => {
  api();
  const view = render(<ModelPredictForm model={{ id: "mdl-1", name: "risk" }} onResult={() => {}} />);
  const payload = view.getByLabelText("Request payload") as HTMLTextAreaElement;
  assert.equal(payload.value, "");
  assert.notEqual(payload.value.trim(), "{}");
  assert.equal(payload.required, true);
  assert.equal(payload.placeholder, PREDICT_PLACEHOLDER);
  assert.match(payload.placeholder, /^Example of the MLflow scoring format only/);
  assert.ok(view.getByText(/MLmodel file/));
});

test("the placeholder example is valid MLflow scoring JSON after its label line", () => {
  const [, example] = PREDICT_PLACEHOLDER.split("\n");
  const parsed = JSON.parse(example);
  assert.deepEqual(Object.keys(parsed), ["dataframe_split"]);
  assert.deepEqual(Object.keys(parsed.dataframe_split), ["columns", "data"]);
});

test("Predict posts exactly what the user typed to the model's predict route", async () => {
  const calls = api();
  let result: unknown;
  const view = render(<ModelPredictForm model={{ id: "mdl 1", name: "risk" }} onResult={(value) => { result = value; }} />);
  fireEvent.change(view.getByLabelText("Request payload"), { target: { value: '{"inputs": [[3, 4]]}' } });
  fireEvent.click(view.getByRole("button", { name: "Predict" }));
  await waitFor(() => assert.ok(calls.some((call) => call.method === "POST")));
  const post = calls.find((call) => call.method === "POST")!;
  assert.equal(post.url, "/api/v1/models/mdl%201/predict");
  assert.deepEqual(JSON.parse(post.body!), { inputs: [[3, 4]] });
  await waitFor(() => assert.deepEqual(result, { predictions: [1] }));
});
