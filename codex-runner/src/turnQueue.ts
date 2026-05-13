import { CosmosClient } from "@azure/cosmos";
import { DefaultAzureCredential } from "@azure/identity";
import {
  SharedTurnQueue,
  type TurnQueueConfig,
} from "../../runner-shared/turnQueue.js";

export * from "../../runner-shared/turnQueue.js";

export class TurnQueue extends SharedTurnQueue {
  constructor(cfg: TurnQueueConfig, provider: "claude" | "codex" | string) {
    super(cfg, provider, { CosmosClient, DefaultAzureCredential });
  }
}
