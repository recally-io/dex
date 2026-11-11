import { useState } from "react";
import { CircleAlert } from "lucide-react";
import type { ChatQuestion } from "../../api/generated/types.gen";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { cn } from "../../lib/utils";

// The question that the agent asked with ask_user. An answered question stays visible until its answer runs,
// so a reload does not hide what the user chose.
export function QuestionCard({
  question,
  pending,
  answer,
}: {
  question: ChatQuestion;
  pending: boolean;
  answer: (text: string) => void;
}) {
  const [other, setOther] = useState("");
  const answered = question.status === "answered";
  // The first option of an action question approves the action. Control checks the answer text.
  const action = question.action;

  return (
    <section
      aria-label="Question from the agent"
      className="mb-3 flex flex-col gap-2 border border-orange bg-orange-tint px-3.5 py-3"
    >
      <div className="flex items-center gap-2 text-[11px] font-bold tracking-wide text-orange">
        <CircleAlert className="size-3" aria-hidden />
        <span className="grow">
          {action === "archive" ? "NEEDS YOU · ARCHIVE BOT" : "NEEDS YOU · QUESTION"}
        </span>
        {answered ? (
          <span className="font-normal text-muted-foreground">Queued · runs after the current reply</span>
        ) : null}
      </div>
      <p className="text-[13.5px] leading-relaxed font-bold">{question.text}</p>
      {answered ? (
        <>
          <p className="text-[13px] leading-relaxed">
            <span className="text-muted-foreground">You answered: </span>
            {question.answer}
          </p>
          {question.outcome ? (
            <p className="text-[13px] leading-relaxed font-medium">
              {question.outcome.replace(/^dex: /, "")}
            </p>
          ) : null}
        </>
      ) : (
        <>
          <div className="flex flex-col gap-1.5">
            {question.options.map((option, index) => (
              <button
                key={option}
                type="button"
                disabled={pending}
                onClick={() => answer(option)}
                className={cn(
                  "flex items-center gap-2.5 border bg-background px-2.5 py-1.5 text-left text-[13px] hover:bg-muted disabled:opacity-50",
                  action && index === 0 ? "border-red text-red" : "border-border",
                )}
              >
                <span className="flex size-5.5 shrink-0 items-center justify-center border border-border text-[11px]">
                  {String.fromCharCode(65 + index)}
                </span>
                <span className="grow">{option}</span>
              </button>
            ))}
          </div>
          <form
            className="flex gap-2"
            onSubmit={(event) => {
              event.preventDefault();

              if (other.trim()) answer(other.trim());
            }}
          >
            <Input
              aria-label="Other answer"
              placeholder="Pick one, or type another answer"
              value={other}
              maxLength={2000}
              onChange={(event) => setOther(event.target.value)}
            />
            <Button type="submit" disabled={pending || !other.trim()}>
              Answer
            </Button>
          </form>
        </>
      )}
    </section>
  );
}
