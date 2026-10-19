// A-NAS shim for the @hermes/shared barrel: just what the vendored transcript
// model and types import, so the rest of the shared package stays out.
export { skillInvocationText } from "./skill-scaffold";
export type {
  BillingBlock,
  ConnectionRequestPayload,
  FreeTierChallengePayload,
  MessageCompletePayload,
  PersistedTurn,
  ToolLabel,
} from "./gateway-contract.generated";
