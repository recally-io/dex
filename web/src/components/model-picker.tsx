import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, Check, Search } from "lucide-react";
import { listModelsOptions } from "../api/generated/@tanstack/react-query.gen";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Failure, Loading } from "./feedback";

export function ModelPicker({
  provider,
  modelId,
  onSelect,
  disabled = false,
}: {
  provider: string;
  modelId: string;
  onSelect: (provider: string, id: string) => void;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const models = useQuery(listModelsOptions());

  const filtered = models.data?.models.filter((m) =>
    `${m.provider} ${m.name} ${m.id}`.toLowerCase().includes(search.toLowerCase()),
  );

  const groups = Object.groupBy(filtered ?? [], (m) => m.provider);

  return (
    <>
      <Button
        variant="ghost"
        size="sm"
        className="min-w-0 max-w-full"
        onClick={() => setOpen(true)}
        disabled={disabled}
      >
        <span className="max-w-48 truncate">{modelId || "Choose a model"}</span>
        <ChevronDown className="size-3.5" />
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Choose a model</DialogTitle>
            <DialogDescription>Applies to all chats from their next message.</DialogDescription>
          </DialogHeader>
          <div className="relative">
            <Search className="absolute left-3 top-2.5 size-4 text-ink-3" />
            <Input
              className="pl-9"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search models…"
              aria-label="Search models"
            />
          </div>
          <div className="max-h-80 overflow-auto">
            {models.isPending ? (
              <Loading rows={2} />
            ) : models.isError ? (
              <Failure error={models.error} retry={() => void models.refetch()} />
            ) : !models.data?.models.length ? (
              <p className="p-5 text-ink-2">No models available. Add a provider in Settings.</p>
            ) : !filtered?.length ? (
              <p className="p-5 text-ink-2">No models match your search.</p>
            ) : (
              Object.entries(groups).map(([group, items]) => (
                <div key={group} className="mb-4">
                  <p className="px-2 py-2 text-xs font-medium text-ink-2">{group}</p>
                  {items?.map((m) => (
                    <button
                      key={m.id}
                      className="flex w-full items-center gap-3 rounded-lg p-3 text-left hover:bg-hover"
                      onClick={() => {
                        onSelect(m.provider, m.id);
                        setOpen(false);
                      }}
                    >
                      <span className="flex-1">
                        <span className="block font-medium">{m.name}</span>
                        <span className="block text-xs text-ink-2">{m.id}</span>
                      </span>
                      {provider === m.provider && modelId === m.id ? (
                        <Check className="size-4 text-brand" />
                      ) : null}
                    </button>
                  ))}
                </div>
              ))
            )}
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
