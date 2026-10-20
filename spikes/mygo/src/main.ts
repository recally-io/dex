import { Agent, events, type Progress } from "./mygo";
import { Channel } from "mygo-runtime";

const out = document.querySelector<HTMLPreElement>("#out")!;
const topic = document.querySelector<HTMLInputElement>("#topic")!;
const write = (value: unknown) => { out.textContent += `${JSON.stringify(value)}\n`; };

events.agentProgress.on((value: Progress) => write({ event: value }));
document.querySelector("#ping")!.addEventListener("click", async () => write(await Agent.ping(topic.value)));
document.querySelector("#stream")!.addEventListener("click", async () => {
  const channel = new Channel<Progress>(write);
  await Agent.stream(topic.value, channel);
});
