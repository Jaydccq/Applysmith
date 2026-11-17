import type { ProviderAccount, ProviderAccounts, ProviderUsage, ProviderUsageWindow } from "../wire";
import { bool, list, num, object, str, time, type Decoder } from "./decode";

const usageWindow: Decoder<ProviderUsageWindow> = object<ProviderUsageWindow>({ name: str }, { used: num, limit: num, remaining: num, usedPercent: num, resetsAt: time });
const usage: Decoder<ProviderUsage> = object<ProviderUsage>({ status: str, message: str, windows: list(usageWindow) }, { checkedAt: time });
export const providerAccount: Decoder<ProviderAccount> = object<ProviderAccount>({
  id: str, name: str, mode: str, installed: bool, credentialStatus: str,
  keyConfigured: bool, cookieConfigured: bool, cookieSupported: bool, workspaceConfigured: bool,
  note: str, usage,
}, { loginCommand: str, consoleURL: str, authFilePresent: bool });
export const providerAccountList: Decoder<ProviderAccounts> = object<ProviderAccounts>({ accounts: list(providerAccount) });
