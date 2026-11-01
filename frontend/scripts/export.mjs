import { cp, mkdir, rm } from "node:fs/promises";
const target = "../go/cmd/gateway/frontend-dist";
await rm(target, { recursive: true, force: true });
await mkdir(target, { recursive: true });
await cp("out", target, { recursive: true });
await cp("placeholder.txt", `${target}/placeholder.txt`);
