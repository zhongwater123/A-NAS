"""What the member answers when the assistant asks back (Hermes's clarify tool).

Hermes sends a ``clarify`` server request with 1–5 questions, each ``{qid, question, choices?}``
(the first of several choices marked as recommended), and takes the response ``{"answers":
{qid: text | None}, "outcome": ...}``; a response without answers cancels them all
(tui_gateway/contracts/server_requests.py, tools/clarify_tool.py). A scenario says how the member
answers (asserts.Turn.clarify):

  cancel  close the card without answering (the default)
  wait    do not answer: Hermes waits out its own deadline
  first   the first choice of every question, the recommended one; a question without choices
          is skipped
  TEXT    for a question with choices, the first choice containing TEXT; otherwise TEXT itself
"""
from __future__ import annotations


def clarify_reply(how: str, questions: list[dict]) -> dict | None:
    """The response to send, or None to send nothing (wait)."""
    if how == "wait":
        return None
    if how == "cancel":
        return {"outcome": "cancelled"}
    answers: dict[str, str | None] = {}
    for q in questions:
        choices = [str(c) for c in q.get("choices") or []]
        if how == "first":
            answers[q["qid"]] = choices[0] if choices else None
        else:
            answers[q["qid"]] = next((c for c in choices if how in c), how)
    return {"answers": answers, "outcome": "submitted"}


def described(questions: list[dict], reply: dict | None) -> list[dict]:
    """The questions with the member's answers, for the run record and the judge."""
    answers = (reply or {}).get("answers") or {}
    return [{"question": q.get("question"), "choices": q.get("choices"), "answer": answers.get(q.get("qid"))}
            for q in questions]
