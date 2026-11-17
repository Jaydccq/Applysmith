// Decoders for the remote-access section of Settings and the header indicator.
// Each mirrors a struct of internal/api/wire/remote.go.
import type { RemoteDevice, RemoteDeviceList, RemoteEvent, RemoteEventList, RemoteListener, RemoteOffer, RemoteQR, RemoteRevoked, RemoteStatus, RemoteTunnel } from "../wire";
import { bool, list, object, safeInt, str, time, type Decoder } from "./decode";

const remoteListener: Decoder<RemoteListener> = object<RemoteListener>({ name: str, address: str });

const remoteTunnel: Decoder<RemoteTunnel> = object<RemoteTunnel>(
  { state: str, publicUrl: str, version: str, consecutiveFailures: safeInt },
  { reason: str, error: str },
);

export const remoteStatus: Decoder<RemoteStatus> = object<RemoteStatus>(
  { mode: str, listeners: list(remoteListener), tunnel: remoteTunnel, allowScreenshots: bool, devices: safeInt, connected: safeInt },
  { error: str },
);

export const remoteQR: Decoder<RemoteQR> = object<RemoteQR>({ size: safeInt, rows: list(str) });

export const remoteOffer: Decoder<RemoteOffer> = object<RemoteOffer>({ link: str, expiresAt: time, qr: remoteQR });

const remoteDevice: Decoder<RemoteDevice> = object<RemoteDevice>({ id: str, label: str, createdAt: time, network: str, connected: bool }, { lastSeenAt: time });

export const remoteDeviceList: Decoder<RemoteDeviceList> = object<RemoteDeviceList>({ devices: list(remoteDevice) });

export const remoteRevoked: Decoder<RemoteRevoked> = object<RemoteRevoked>({ revoked: safeInt });

const remoteEvent: Decoder<RemoteEvent> = object<RemoteEvent>({ at: time, kind: str, source: str, detail: str }, { deviceId: str });

export const remoteEventList: Decoder<RemoteEventList> = object<RemoteEventList>({ events: list(remoteEvent) });
