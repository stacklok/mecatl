// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { joinTranscript, useVoiceInput } from "./use-voice-input";

const originalPermissions = Object.getOwnPropertyDescriptor(navigator, "permissions");

afterEach(() => {
  cleanup();
  Reflect.deleteProperty(window, "SpeechRecognition");
  Reflect.deleteProperty(window, "webkitSpeechRecognition");
  if (originalPermissions) {
    Object.defineProperty(navigator, "permissions", originalPermissions);
  } else {
    Reflect.deleteProperty(navigator, "permissions");
  }
});

describe("voice input", () => {
  it("appends a transcript to existing input", () => {
    expect(joinTranscript("Explain this", "in simple terms")).toBe("Explain this in simple terms");
  });

  it("does not add leading space to an empty prompt", () => {
    expect(joinTranscript("", " hello world")).toBe("hello world");
  });

  it("keeps the typed draft after denied speech recognition", () => {
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    class DeniedRecognition {
      start() {
        throw new DOMException("Permission denied", "NotAllowedError");
      }
      stop() {}
    }
    Object.defineProperty(window, "SpeechRecognition", {
      configurable: true,
      value: DeniedRecognition,
    });
    const { result } = renderHook(() => {
      const [draft, setDraft] = useState("Typed draft");
      return { draft, voice: useVoiceInput(draft, setDraft) };
    });
    act(() => result.current.voice.toggle());
    expect(result.current.draft).toBe("Typed draft");
    expect(result.current.voice.isListening).toBe(false);
    expect(result.current.voice.errorMessage).toMatch(/microphone|permission/i);
  });

  it("commits final speech but never commits interim speech", () => {
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    let recognition: TestRecognition | undefined;
    class TestRecognition {
      onresult: ((event: unknown) => void) | null = null;
      onerror: (() => void) | null = null;
      onend: (() => void) | null = null;
      continuous = false;
      interimResults = false;
      lang = "";
      constructor() {
        recognition = this;
      }
      start() {}
      stop() {
        this.onend?.();
      }
    }
    Object.defineProperty(window, "SpeechRecognition", {
      configurable: true,
      value: TestRecognition,
    });
    const { result } = renderHook(() => {
      const [draft, setDraft] = useState("Typed draft");
      return { draft, voice: useVoiceInput(draft, setDraft) };
    });
    act(() => result.current.voice.toggle());
    act(() =>
      recognition?.onresult?.({
        resultIndex: 0,
        results: [{ 0: { transcript: "maybe" }, isFinal: false }],
      }),
    );
    expect(result.current.draft).toBe("Typed draft");
    expect(result.current.voice.interimTranscript).toBe("maybe");
    act(() =>
      recognition?.onresult?.({
        resultIndex: 0,
        results: [{ 0: { transcript: "final" }, isFinal: true }],
      }),
    );
    expect(result.current.draft).toBe("Typed draft final");
    act(() => result.current.voice.stop());
    expect(result.current.voice.isListening).toBe(false);
  });

  it("explains unsupported speech without changing the draft", () => {
    const { result } = renderHook(() => {
      const [draft, setDraft] = useState("Typed draft");
      return { draft, voice: useVoiceInput(draft, setDraft) };
    });
    act(() => result.current.voice.toggle());
    expect(result.current.draft).toBe("Typed draft");
    expect(result.current.voice.errorMessage).toMatch(/not available|unsupported/i);
  });

  it("uses the prefixed browser API and handles an asynchronous permission denial", async () => {
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    Object.defineProperty(navigator, "permissions", {
      configurable: true,
      value: { query: vi.fn().mockRejectedValue(new Error("query unavailable")) },
    });
    let recognition: PrefixedRecognition | undefined;
    class PrefixedRecognition {
      onresult = null;
      onend: (() => void) | null = null;
      onerror: ((event: { error: string }) => void) | null = null;
      continuous = false;
      interimResults = false;
      lang = "";
      constructor() {
        recognition = this;
      }
      start() {}
      stop() {}
    }
    Object.defineProperty(window, "webkitSpeechRecognition", {
      configurable: true,
      value: PrefixedRecognition,
    });
    const { result } = renderHook(() => {
      const [draft, setDraft] = useState("Typed draft");
      return { draft, voice: useVoiceInput(draft, setDraft) };
    });
    act(() => result.current.voice.toggle());
    expect(result.current.voice.isListening).toBe(true);
    await act(async () => Promise.resolve());
    expect(result.current.voice.isListening).toBe(true);
    act(() => recognition?.onerror?.({ error: "not-allowed" }));
    expect(result.current.voice.isListening).toBe(false);
    expect(result.current.draft).toBe("Typed draft");
    expect(result.current.voice.errorMessage).toMatch(/permission/i);
  });

  it("reports a pre-denied microphone when recognition never calls back", async () => {
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    const query = vi.fn().mockResolvedValue({ state: "denied" });
    Object.defineProperty(navigator, "permissions", {
      configurable: true,
      value: { query },
    });
    let recognition: SilentRecognition | undefined;
    class SilentRecognition {
      onresult: ((event: unknown) => void) | null = null;
      onend: (() => void) | null = null;
      onerror: (() => void) | null = null;
      continuous = false;
      interimResults = false;
      lang = "";
      start = vi.fn();
      stop = vi.fn();
      constructor() {
        recognition = this;
      }
    }
    Object.defineProperty(window, "SpeechRecognition", {
      configurable: true,
      value: SilentRecognition,
    });
    const { result } = renderHook(() => {
      const [draft, setDraft] = useState("Typed draft");
      return { draft, voice: useVoiceInput(draft, setDraft) };
    });

    act(() => result.current.voice.toggle());
    expect(recognition?.start).toHaveBeenCalledOnce();
    expect(query).toHaveBeenCalledWith({ name: "microphone" });
    await waitFor(() => {
      expect(result.current.voice.isListening).toBe(false);
      expect(result.current.voice.errorMessage).toMatch(/microphone|permission/i);
    });
    expect(recognition?.stop).toHaveBeenCalledOnce();
    expect(result.current.draft).toBe("Typed draft");

    act(() =>
      recognition?.onresult?.({
        resultIndex: 0,
        results: [{ 0: { transcript: "late words" }, isFinal: true }],
      }),
    );
    expect(result.current.draft).toBe("Typed draft");
  });

  it("ignores recognition and permission callbacks after leaving the composer", async () => {
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    let resolvePermission: (status: { state: string }) => void = () => {};
    const permission = new Promise<{ state: string }>((resolve) => {
      resolvePermission = resolve;
    });
    Object.defineProperty(navigator, "permissions", {
      configurable: true,
      value: { query: () => permission },
    });
    let recognition: SilentRecognition | undefined;
    class SilentRecognition {
      onresult: ((event: unknown) => void) | null = null;
      onend: (() => void) | null = null;
      onerror: (() => void) | null = null;
      continuous = false;
      interimResults = false;
      lang = "";
      start() {}
      stop = vi.fn();
      constructor() {
        recognition = this;
      }
    }
    Object.defineProperty(window, "SpeechRecognition", {
      configurable: true,
      value: SilentRecognition,
    });
    const onChange = vi.fn();
    const { result, unmount } = renderHook(() => useVoiceInput("Typed draft", onChange));
    act(() => result.current.toggle());
    unmount();
    act(() =>
      recognition?.onresult?.({
        resultIndex: 0,
        results: [{ 0: { transcript: "late words" }, isFinal: true }],
      }),
    );
    expect(onChange).not.toHaveBeenCalled();
    await act(async () => resolvePermission({ state: "denied" }));
    expect(recognition?.stop).toHaveBeenCalledOnce();
  });
});
