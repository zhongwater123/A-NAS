// Every sentence Venus shows, by key (PRD appendix A): feature code names a
// key and never writes its own Chinese. One file a page, so people working
// on different pages add their sentences apart; a key is in one file only
// (messages.test.ts).
import { cut } from "./cut";
import { prepare } from "./prepare";
import { shared } from "./shared";
import { start } from "./start";

export const pages = { shared, start, prepare, cut };

export const messages = { ...shared, ...start, ...prepare, ...cut };
