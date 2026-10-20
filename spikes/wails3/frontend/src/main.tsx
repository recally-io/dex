import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { JSONStream } from "@wailsio/runtime";
import { TranscriptService } from "../bindings/dex-wails3-spike";

function App() {
  const [chunks, setChunks] = useState<string[]>([]);
  useEffect(() => {
    const stream = JSONStream("transcript");
    stream.onmessage = (event: MessageEvent<{ seq: number; text: string }>) => {
      setChunks((old) => [...old.slice(-7), `${event.data.seq}: ${event.data.text}`]);
    };
    return () => stream.close();
  }, []);
  return <main><h1>Dex Wails 3 spike</h1><button onClick={() => TranscriptService.Ping().then(alert)}>Ping binding</button><pre>{chunks.join("\n")}</pre></main>;
}

createRoot(document.getElementById("root")!).render(<App />);
