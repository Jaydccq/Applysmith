// Decoders for the Settings sections that read the system: the provider test
// start, the workspace, the log, and the caller's identity.
import type { ChatGPTModels, ChatGPTSignInStarted, ChatGPTSignOutResult, ChatGPTStatus, CLIInstall, LLMTestStarted, LogLines, RemoteMe, SystemInfo } from "../wire";
import { bool, object, orEmpty, str, type Decoder } from "./decode";

export const llmTestStarted: Decoder<LLMTestStarted> = object<LLMTestStarted>({ provider: str });

export const chatgptStatus: Decoder<ChatGPTStatus> = object<ChatGPTStatus>({ signedIn: bool, planUsage: bool, pending: bool }, { account: str, message: str });

export const chatgptSignInStarted: Decoder<ChatGPTSignInStarted> = object<ChatGPTSignInStarted>({ url: str });

export const chatgptSignOutResult: Decoder<ChatGPTSignOutResult> = object<ChatGPTSignOutResult>({ revoked: bool });

export const chatgptModels: Decoder<ChatGPTModels> = object<ChatGPTModels>({ models: orEmpty(object({ slug: str })) });

export const systemInfo: Decoder<SystemInfo> = object<SystemInfo>({ workspacePath: str });

export const logLines: Decoder<LogLines> = object<LogLines>({ lines: orEmpty(str) });

export const remoteMe: Decoder<RemoteMe> = object<RemoteMe>({ kind: str }, { deviceId: str, label: str });

export const cliInstall: Decoder<CLIInstall> = object<CLIInstall>({
  available: bool,
  state: str,
  link: str,
  target: str,
  onPath: bool,
  profileFile: str,
  profileLine: str,
});
