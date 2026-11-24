import "./setup-dom";
import assert from "node:assert/strict";
import { afterEach, test } from "node:test";
import React from "react";
import { render, fireEvent, waitFor, cleanup } from "@testing-library/react";
import { ModelPredictForm, PREDICT_PLACEHOLDER, PREDICT_SAMPLE_LABELS } from "../features/operations/Resources";

Object.assign(globalThis, { FormData: window.FormData, self: window });
afterEach(cleanup);

type Sample = { status: number; body: unknown };
const NOT_CONFIGURED: Sample = { status: 409, body: { error: "not_configured", message: "MLFLOW_URL is not set", steps: [] } };

// The stub answers the input-example route with sample and every predict with
// a prediction, and records each call.
function api(sample: Sample = NOT_CONFIGURED) {
  const calls: Array<{ url: string; method: string; body: string | undefined }> = [];
  globalThis.fetch = async (input, init) => {
    const url = String(input), method = init?.method || "GET";
    calls.push({ url, method, body: init?.body ? String(init.body) : undefined });
    const answer = url.endsWith("/input-example") ? sample : { status: 200, body: { predictions: [1] } };
    return { ok: answer.status < 300, status: answer.status, text: async () => JSON.stringify(answer.body) } as Response;
  };
  return calls;
}
const SAVED = { model_id: "mdl-1", name: "risk", version: "3", source: "input_example", payload: { dataframe_split: { columns: ["tenure", "seats"], data: [[12.5, 4]] } } };
const DERIVED = { model_id: "mdl-1", name: "risk", version: "3", source: "signature", payload: { dataframe_split: { columns: ["tenure", "plan"], data: [[0.5, "string"]] } } };
const payloadField = (view: ReturnType<typeof render>) => view.getByLabelText("Request payload") as HTMLTextAreaElement;

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

test("the dialog asks the model's input-example route when it opens", async () => {
  const calls = api({ status: 200, body: SAVED });
  render(<ModelPredictForm model={{ id: "mdl 1", name: "risk" }} onResult={() => {}} />);
  await waitFor(() => assert.deepEqual(calls.map((call) => [call.method, call.url]), [["GET", "/api/v1/models/mdl%201/input-example"]]));
});

test("a saved example is offered as a labelled placeholder and is never prefilled", async () => {
  api({ status: 200, body: SAVED });
  const view = render(<ModelPredictForm model={{ id: "mdl-1", name: "risk" }} onResult={() => {}} />);
  await waitFor(() => assert.ok(view.getByRole("button", { name: "Use example" })));
  const payload = payloadField(view);
  assert.equal(payload.value, "");
  const [label, ...example] = payload.placeholder.split("\n");
  assert.equal(label, PREDICT_SAMPLE_LABELS.input_example);
  assert.match(label, /saved input example/);
  assert.deepEqual(JSON.parse(example.join("\n")), SAVED.payload);
  assert.ok(view.getByText(PREDICT_SAMPLE_LABELS.input_example));
});

test("Use example fills the payload with the saved example, and Predict sends it", async () => {
  const calls = api({ status: 200, body: SAVED });
  const view = render(<ModelPredictForm model={{ id: "mdl-1", name: "risk" }} onResult={() => {}} />);
  await waitFor(() => assert.ok(view.getByRole("button", { name: "Use example" })));
  fireEvent.change(payloadField(view), { target: { value: '{"inputs": [[9]]}' } });
  fireEvent.click(view.getByRole("button", { name: "Use example" }));
  assert.deepEqual(JSON.parse(payloadField(view).value), SAVED.payload);
  fireEvent.click(view.getByRole("button", { name: "Predict" }));
  await waitFor(() => assert.ok(calls.some((call) => call.method === "POST")));
  const post = calls.find((call) => call.method === "POST")!;
  assert.equal(post.url, "/api/v1/models/mdl-1/predict");
  assert.deepEqual(JSON.parse(post.body!), SAVED.payload);
});

test("a skeleton derived from the signature is labelled as derived with placeholder values", async () => {
  api({ status: 200, body: DERIVED });
  const view = render(<ModelPredictForm model={{ id: "mdl-1", name: "risk" }} onResult={() => {}} />);
  await waitFor(() => assert.ok(view.getByRole("button", { name: "Use example" })));
  const payload = payloadField(view);
  assert.equal(payload.value, "");
  const [label, ...example] = payload.placeholder.split("\n");
  assert.equal(label, PREDICT_SAMPLE_LABELS.signature);
  assert.match(label, /Derived from this model's signature/);
  assert.match(label, /placeholders/);
  assert.doesNotMatch(label, /saved input example/);
  assert.deepEqual(JSON.parse(example.join("\n")), DERIVED.payload);
  assert.ok(view.getByText(PREDICT_SAMPLE_LABELS.signature));
  fireEvent.click(view.getByRole("button", { name: "Use example" }));
  assert.deepEqual(JSON.parse(payloadField(view).value), DERIVED.payload);
});

for (const [name, sample] of Object.entries({
  "MLflow is not configured (409)": NOT_CONFIGURED,
  "the model has no sample (404)": { status: 404, body: { error: "input_example_unavailable", message: "none" } },
  "MLflow is unreachable (502)": { status: 502, body: { error: "mlflow_unavailable", message: "down" } },
  "the answer has an unknown source": { status: 200, body: { ...SAVED, source: "guess" } },
  "the answer has no object payload": { status: 200, body: { ...SAVED, payload: "text" } },
} as Record<string, Sample>)) {
  test(`the generic example stays when ${name}`, async () => {
    const calls = api(sample);
    const view = render(<ModelPredictForm model={{ id: "mdl-1", name: "risk" }} onResult={() => {}} />);
    await waitFor(() => assert.equal(calls.length, 1));
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.equal(payloadField(view).placeholder, PREDICT_PLACEHOLDER);
    assert.equal(payloadField(view).value, "");
    assert.equal(view.queryByRole("button", { name: "Use example" }), null);
    assert.equal(view.queryByRole("alert"), null);
  });
}

test("the generic example stays when the request fails outright", async () => {
  globalThis.fetch = async () => { throw new TypeError("network down"); };
  const view = render(<ModelPredictForm model={{ id: "mdl-1", name: "risk" }} onResult={() => {}} />);
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(payloadField(view).placeholder, PREDICT_PLACEHOLDER);
  assert.equal(view.queryByRole("button", { name: "Use example" }), null);
});
